package smtp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/mail"
	"strings"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"

	"github.com/krisamin/mail/internal/delivery"
	"github.com/krisamin/mail/internal/guard"
	"github.com/krisamin/mail/internal/metric"
	"github.com/krisamin/mail/internal/policy"
	"github.com/krisamin/mail/internal/store"
)

// SubmissionBackend is the mail submission backend (port 587 role).
// Unlike inbound (MX), **AUTH is required** — it's the door where our users
// authenticate with an app password and send mail out. DD-02: mail app auth
// = app password.
//
// Routing: recipients on local domains are delivered directly; external
// domains go to the outbound queue (Phase 2-3). Without a queue
// (EnqueueDisabled), external domains are rejected with 550.
type SubmissionBackend struct {
	store    store.Store
	hostname string
	// enqueueEnabled=false rejects recipients on external domains
	// (configurations where no outbound queue worker runs — e.g. dev
	// without relay configured).
	enqueueEnabled bool
	// limiter defends against auth brute force (per IP).
	limiter *guard.Limiter
}

// NewSubmissionBackend creates a submission backend.
// enqueueEnabled: whether recipients on external domains go to the outbound queue.
func NewSubmissionBackend(st store.Store, hostname string, enqueueEnabled bool) *SubmissionBackend {
	return &SubmissionBackend{
		store: st, hostname: hostname, enqueueEnabled: enqueueEnabled,
		limiter: guard.NewLimiter(),
	}
}

// NewSession is called per connection.
func (b *SubmissionBackend) NewSession(c *gosmtp.Conn) (gosmtp.Session, error) {
	remote := ""
	if c != nil && c.Conn() != nil {
		remote = c.Conn().RemoteAddr().String()
	}
	helo := ""
	if c != nil {
		helo = c.Hostname()
	}
	return &SubmissionSession{backend: b, remoteAddr: remote, heloName: helo}, nil
}

// SubmissionSession is an authenticated submission transaction.
type SubmissionSession struct {
	backend    *SubmissionBackend
	remoteAddr string
	heloName   string

	user        *store.Account  // populated on successful auth
	accountAddr string          // address used to authenticate (for envelope-from validation)
	perm        policy.Effective // group stack folded once, at auth time

	from         string
	senderDomain *store.Domain // domain of `from` (permission switches live here)
	rcptList     []rcpt        // local delivery targets
	external     []string      // external domains → outbound queue targets
	counted      int           // recipients already charged to the daily limit
}

var _ gosmtp.Session = (*SubmissionSession)(nil)
var _ gosmtp.AuthSession = (*SubmissionSession)(nil)

// AuthMechanisms lists the supported SASL mechanisms.
func (s *SubmissionSession) AuthMechanisms() []string {
	return []string{sasl.Plain}
}

// Auth returns the SASL server. PLAIN = address + app password.
// Per-IP brute-force defense: repeated failures trigger a temporary block.
func (s *SubmissionSession) Auth(mech string) (sasl.Server, error) {
	if mech != sasl.Plain {
		return nil, gosmtp.ErrAuthUnsupported
	}
	return sasl.NewPlainServer(func(identity, username, password string) error {
		if identity != "" && identity != username {
			return errors.New("identity not supported")
		}
		ip := remoteIP(s.remoteAddr)
		ipKey := ""
		if ip != nil {
			ipKey = "ip:" + guard.KeyForIP(ip.String())
		}
		acctKey := "acct:" + strings.ToLower(username)
		if !s.backend.limiter.Allow(ipKey) || !s.backend.limiter.Allow(acctKey) {
			metric.AuthTotal.WithLabelValues("submission", "blocked").Inc()
			return &gosmtp.SMTPError{
				Code:         421,
				EnhancedCode: gosmtp.EnhancedCode{4, 7, 0},
				Message:      "too many failed attempts, try again later",
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()

		u, err := s.backend.store.AuthenticateAppPassword(ctx, username, password, store.ScopeSMTP)
		if err != nil {
			// Correct secret, missing permission — a clean 535 without touching
			// the brute-force counter.
			if errors.Is(err, store.ErrScopeDenied) {
				metric.PolicyBlockTotal.WithLabelValues("scope_missing", "submission").Inc()
				return &gosmtp.SMTPError{
					Code:         535,
					EnhancedCode: gosmtp.EnhancedCode{5, 7, 8},
					Message:      "this app password is not allowed to send mail",
				}
			}
			if errors.Is(err, store.ErrAuthFailed) || errors.Is(err, store.ErrNotFound) {
				metric.AuthTotal.WithLabelValues("submission", "fail").Inc()
				s.backend.limiter.Fail(ipKey)
				s.backend.limiter.Fail(acctKey)
				return &gosmtp.SMTPError{
					Code:         535,
					EnhancedCode: gosmtp.EnhancedCode{5, 7, 8},
					Message:      "authentication failed",
				}
			}
			return err
		}
		metric.AuthTotal.WithLabelValues("submission", "ok").Inc()
		s.backend.limiter.Success(ipKey)
		s.backend.limiter.Success(acctKey)
		s.user = u
		s.accountAddr = strings.ToLower(username)
		// Permissions come from the group stack; fold it once here rather
		// than per recipient.
		if groupList, gerr := s.backend.store.GroupListForAccount(ctx, u.ID); gerr == nil {
			s.perm = policy.Resolve(groupList)
		} else {
			log.Printf("submission: group resolve failed user=%s: %v", username, gerr)
		}
		return nil
	}), nil
}

// Mail requires AUTH and the envelope from must be an address owned by the
// authenticated account (sender-forgery prevention). Own address or an alias
// bound to the account (wildcards included).
func (s *SubmissionSession) Mail(from string, opts *gosmtp.MailOptions) error {
	if s.user == nil {
		return gosmtp.ErrAuthRequired
	}
	if strings.ToLower(from) != s.accountAddr {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		ok, err := s.backend.store.CanSendAs(ctx, s.user.ID, from)
		cancel()
		if err != nil {
			return err
		}
		if !ok {
			return &gosmtp.SMTPError{
				Code:         553,
				EnhancedCode: gosmtp.EnhancedCode{5, 7, 1},
				Message:      "sender address must match authenticated user or an owned alias",
			}
		}
	}
	s.from = from

	// The sending domain carries the operator's kill switch; look it up once
	// here rather than per recipient. A missing domain is not fatal — sender
	// ownership was already proven, so the account switch alone decides.
	if at := strings.LastIndex(from, "@"); at >= 0 {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		if d, err := s.backend.store.FindDomain(ctx, strings.ToLower(from[at+1:])); err == nil {
			s.senderDomain = d
		}
		cancel()
	}
	return nil
}

// refuse turns a policy verdict into an SMTP error and records it.
func refuse(v policy.Verdict, path string, code int, enhanced gosmtp.EnhancedCode) error {
	metric.PolicyBlockTotal.WithLabelValues(string(v.Reason), path).Inc()
	return &gosmtp.SMTPError{Code: code, EnhancedCode: enhanced, Message: v.Message}
}

// Rcpt classifies the recipient: local domain → verify user, external → rejected until Phase 2-3.
func (s *SubmissionSession) Rcpt(to string, opts *gosmtp.RcptOptions) error {
	if s.user == nil {
		return gosmtp.ErrAuthRequired
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()

	at := strings.LastIndex(to, "@")
	if at < 0 {
		return &gosmtp.SMTPError{
			Code:         501,
			EnhancedCode: gosmtp.EnhancedCode{5, 1, 3},
			Message:      "invalid recipient address",
		}
	}
	domain := to[at+1:]

	if _, err := s.backend.store.FindDomain(ctx, domain); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// external domain → outbound queue (only when enabled)
			if !s.backend.enqueueEnabled {
				return &gosmtp.SMTPError{
					Code:         550,
					EnhancedCode: gosmtp.EnhancedCode{5, 7, 1},
					Message:      "relaying to external domainList is disabled",
				}
			}
			if v := policy.Send(s.user, s.perm, s.senderDomain, true); !v.Allowed {
				return refuse(v, "submission", 550, gosmtp.EnhancedCode{5, 7, 1})
			}
			s.external = append(s.external, to)
			return nil
		}
		return err
	}

	u, err := s.backend.store.ResolveAddress(ctx, to)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &gosmtp.SMTPError{
				Code:         550,
				EnhancedCode: gosmtp.EnhancedCode{5, 1, 1},
				Message:      "no such user",
			}
		}
		return err
	}
	if v := policy.Send(s.user, s.perm, s.senderDomain, false); !v.Allowed {
		return refuse(v, "submission", 550, gosmtp.EnhancedCode{5, 7, 1})
	}
	s.rcptList = append(s.rcptList, rcpt{address: to, user: u})
	return nil
}

// Data receives the body, delivers to local recipients, and enqueues external
// recipients to the outbound queue.
//
// Failure policy: if even one delivery/enqueue fails, reject the whole
// transaction with 451 — duplicate delivery from a client resend
// (at-least-once) beats returning 250 and silently losing part of the mail.
func (s *SubmissionSession) Data(r io.Reader) error {
	if len(s.rcptList) == 0 && len(s.external) == 0 {
		return &gosmtp.SMTPError{
			Code:         503,
			EnhancedCode: gosmtp.EnhancedCode{5, 5, 1},
			Message:      "no valid recipients",
		}
	}

	// Daily limit. Charged here, once the recipient count is final, and given
	// back if the transaction fails below — the limit should measure mail that
	// actually went out, not attempts.
	if n := len(s.rcptList) + len(s.external); n > 0 && s.perm.DailySendLimit != nil {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		used, err := s.backend.store.BumpSendCounter(ctx, s.user.ID, n)
		cancel()
		if err != nil {
			log.Printf("submission: send counter failed user=%s: %v", s.accountAddr, err)
		} else {
			s.counted = n
			if v := policy.DailyLimit(s.perm.DailySendLimit, used); !v.Allowed {
				s.releaseCount()
				return refuse(v, "submission", 451, gosmtp.EnhancedCode{4, 7, 0})
			}
		}
	}

	raw, err := io.ReadAll(r)
	if err != nil {
		s.releaseCount()
		return err
	}

	// Header From validation — trusting only the envelope (Mail) would let
	// someone stamp another person's address into the header From:, and the
	// queue worker would even add a DKIM signature with the domain key.
	// The header From must also be an address owned by the authenticated
	// account (spoofing prevention).
	if headerFrom := headerFromAddress(raw); headerFrom != "" && headerFrom != s.accountAddr {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		ok, err := s.backend.store.CanSendAs(ctx, s.user.ID, headerFrom)
		cancel()
		if err != nil {
			return err
		}
		if !ok {
			return &gosmtp.SMTPError{
				Code:         553,
				EnhancedCode: gosmtp.EnhancedCode{5, 7, 1},
				Message:      "header From must match authenticated user or an owned alias",
			}
		}
	}

	// delivery logic is shared with the inbound session
	inbound := &Session{
		backend:    &Backend{store: s.backend.store, hostname: s.backend.hostname},
		remoteAddr: s.remoteAddr,
		heloName:   s.heloName,
		from:       s.from,
	}

	now := timeNow()
	delivered := 0
	for _, rc := range s.rcptList {
		stamped := inbound.receivedHeader(rc.address, now)
		stamped = append(stamped, raw...)
		if err := inbound.deliver(rc, "INBOX", stamped, now); err != nil {
			if errors.Is(err, delivery.ErrQuotaExceeded) {
				log.Printf("submission: quota reject to=%s from=%s", rc.address, s.from)
				return &gosmtp.SMTPError{
					Code:         452,
					EnhancedCode: gosmtp.EnhancedCode{4, 2, 2},
					Message:      "recipient mailbox is full",
				}
			}
			log.Printf("submission: delivery failed to=%s from=%s: %v", rc.address, s.from, err)
			return &gosmtp.SMTPError{
				Code:         451,
				EnhancedCode: gosmtp.EnhancedCode{4, 3, 0},
				Message:      "delivery failed, try again later",
			}
		}
		delivered++
	}

	// external recipients → outbound queue (Received header stamped the same
	// way as evidence the message passed through this server)
	enqueued := 0
	if len(s.external) > 0 {
		stamped := inbound.receivedHeader(s.external[0], now)
		stamped = append(stamped, raw...)
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		err := s.backend.store.EnqueueOutbound(ctx, s.from, s.external, stamped)
		cancel()
		if err != nil {
			// Swallowing an enqueue failure with 250 makes the client believe
			// it was sent while the external recipient never receives it —
			// return 451 to induce a retry.
			log.Printf("submission: enqueue failed from=%s rcptList=%v: %v", s.from, s.external, err)
			return &gosmtp.SMTPError{
				Code:         451,
				EnhancedCode: gosmtp.EnhancedCode{4, 3, 0},
				Message:      "queueing failed, try again later",
			}
		}
		enqueued = len(s.external)
	}

	log.Printf("submission: submission complete user=%s local=%d/%d queued=%d/%d size=%d",
		s.accountAddr, delivered, len(s.rcptList), enqueued, len(s.external), len(raw))
	return nil
}

func (s *SubmissionSession) Reset() {
	s.from = ""
	s.senderDomain = nil
	s.rcptList = nil
	s.external = nil
	s.counted = 0
}

// releaseCount gives back the daily-limit slots charged for this transaction.
// Best-effort: a lost refund only makes the limit slightly stricter for a day.
func (s *SubmissionSession) releaseCount() {
	if s.counted == 0 || s.user == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	if err := s.backend.store.ReleaseSendCounter(ctx, s.user.ID, s.counted); err != nil {
		log.Printf("submission: send counter release failed user=%s: %v", s.accountAddr, err)
	}
	cancel()
	s.counted = 0
}

// headerFromAddress extracts the From: address from the message header block,
// lowercased. On parse failure/absence returns "" (caller skips validation —
// envelope validation already passed at the Mail stage).
func headerFromAddress(raw []byte) string {
	headerEnd := bytes.Index(raw, []byte("\r\n\r\n"))
	if headerEnd < 0 {
		headerEnd = len(raw)
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw[:min(headerEnd+4, len(raw))]))
	if err != nil {
		return ""
	}
	fromHeader := msg.Header.Get("From")
	if fromHeader == "" {
		return ""
	}
	addr, err := mail.ParseAddress(fromHeader)
	if err != nil {
		return ""
	}
	return strings.ToLower(addr.Address)
}

func (s *SubmissionSession) Logout() error {
	return nil
}
