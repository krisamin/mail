// Package smtp implements the go-smtp inbound backend on top of store.Store.
//
// Phase 2-1: accepts mail arriving from outside (port 25 MX role), validates
// recipients, and delivers to INBOX. No auth — MX reception is inherently
// anonymous (sender verification belongs to Phase 2-4 SPF/DKIM/DMARC).
// Submission (587, AUTH required) is separate.
package smtp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"

	gosmtp "github.com/emersion/go-smtp"

	"github.com/krisamin/mail/internal/auth"
	"github.com/krisamin/mail/internal/spam"
	"github.com/krisamin/mail/internal/store"
)

// opTimeout is the upper bound for a single SMTP callback's store access.
const opTimeout = 30 * time.Second

// timeNow is a variable so tests can swap it out.
var timeNow = time.Now

// Backend is the inbound (MX) SMTP backend.
type Backend struct {
	store    store.Store
	hostname string // server name stamped into Received headers
	// verifyInbound, when true, runs SPF/DKIM/DMARC verification on inbound
	// mail and attaches an Authentication-Results header (record only).
	verifyInbound bool
	// enforceDMARC, when true, enforces the sender domain's DMARC policy:
	// verdict fail + p=reject → 550 rejection, p=quarantine → deliver to Junk.
	// p=none or no record → record only (previous behavior).
	enforceDMARC bool
	// checker screens connections (DNSBL reject, rDNS/HELO quarantine).
	// nil = screening disabled.
	checker *spam.Checker
}

// NewBackend creates an inbound backend on top of store.
func NewBackend(st store.Store, hostname string) *Backend {
	return &Backend{store: st, hostname: hostname}
}

// WithSpamChecker enables connection screening: DNSBL-listed IPs are rejected
// at MAIL FROM (554); missing FCrDNS + implausible HELO quarantines to Junk.
func (b *Backend) WithSpamChecker(c *spam.Checker) *Backend {
	b.checker = c
	return b
}

// WithInboundVerification enables inbound SPF/DKIM/DMARC verification.
func (b *Backend) WithInboundVerification() *Backend {
	b.verifyInbound = true
	return b
}

// WithDMARCEnforcement enables DMARC policy enforcement (verification is enabled too).
func (b *Backend) WithDMARCEnforcement() *Backend {
	b.verifyInbound = true
	b.enforceDMARC = true
	return b
}

// NewSession is called per connection (gosmtp.Backend interface).
func (b *Backend) NewSession(c *gosmtp.Conn) (gosmtp.Session, error) {
	remote := ""
	if c != nil && c.Conn() != nil {
		remote = c.Conn().RemoteAddr().String()
	}
	helo := ""
	if c != nil {
		helo = c.Hostname()
	}
	return &Session{backend: b, remoteAddr: remote, heloName: helo}, nil
}

// rcpt is a single recipient that passed validation.
type rcpt struct {
	address string
	user    *store.Account
}

// Session is a single SMTP transaction (implements gosmtp.Session).
type Session struct {
	backend    *Backend
	remoteAddr string
	heloName   string

	from     string
	rcptList []rcpt
	// suspicious marks a weak-signal connection (no FCrDNS + bogus HELO) —
	// delivery goes to Junk instead of INBOX.
	suspicious bool
}

var _ gosmtp.Session = (*Session)(nil)

func (s *Session) Mail(from string, opts *gosmtp.MailOptions) error {
	// Connection screening at MAIL FROM — after HELO (so we have the name)
	// but before any recipient/body work is wasted on a listed sender.
	if s.backend.checker != nil {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		ip := remoteIP(s.remoteAddr)

		if v := s.backend.checker.CheckDNSBL(ctx, ip); v.Listed {
			log.Printf("smtp: DNSBL reject ip=%s zone=%s code=%s from=%s", ip, v.Zone, v.Code, from)
			return &gosmtp.SMTPError{
				Code:         554,
				EnhancedCode: gosmtp.EnhancedCode{5, 7, 1},
				Message:      "IP listed by " + v.Zone,
			}
		}
		// weak signals: never reject, quarantine at delivery time
		conn := s.backend.checker.CheckConnection(ctx, ip, s.heloName)
		if conn.Suspicious {
			s.suspicious = true
			log.Printf("smtp: suspicious connection ip=%s helo=%q signals=%v from=%s",
				ip, s.heloName, conn.SignalList, from)
		}
	}
	s.from = from
	return nil
}

// Rcpt verifies the recipient is one of our users. Otherwise 550 5.1.1.
// ★Rejecting here is what prevents backscatter (accept-then-bounce spam).
// Aliases and wildcards are also deliverable (ResolveAddress).
func (s *Session) Rcpt(to string, opts *gosmtp.RcptOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()

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
	s.rcptList = append(s.rcptList, rcpt{address: to, user: u})
	return nil
}

// Data receives the body and delivers it to each recipient's INBOX.
// With DMARC enforcement on: fail+p=reject → 550 rejection,
// fail+p=quarantine → deliver to Junk folder.
func (s *Session) Data(r io.Reader) error {
	if len(s.rcptList) == 0 {
		return &gosmtp.SMTPError{
			Code:         503,
			EnhancedCode: gosmtp.EnhancedCode{5, 5, 1},
			Message:      "no valid recipients",
		}
	}

	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	// SPF/DKIM/DMARC verification → record Authentication-Results + policy decision
	var authHeader []byte
	folder := "INBOX"
	if s.suspicious {
		// connection screening (no FCrDNS + implausible HELO) → quarantine
		folder = "Junk"
	}
	if s.backend.verifyInbound {
		ip := remoteIP(s.remoteAddr)
		vr := auth.VerifyInbound(raw, auth.VerifyOptions{
			RemoteIP:     ip,
			HeloName:     s.heloName,
			EnvelopeFrom: s.from,
			Hostname:     s.backend.hostname,
		})
		authHeader = vr.Header
		log.Printf("smtp: auth verification from=%s ip=%s spf=%v dkim=%v dmarc=%v policy=%s",
			s.from, ip, vr.SPFPass, vr.DKIMPass, vr.DMARCPass, vr.DMARCPolicy)

		// DMARC policy enforcement — follow the policy the sender domain published.
		if s.backend.enforceDMARC && vr.DMARCEvaluated && !vr.DMARCPass {
			switch vr.DMARCPolicy {
			case "reject":
				log.Printf("smtp: DMARC reject from=%s ip=%s", s.from, ip)
				return &gosmtp.SMTPError{
					Code:         550,
					EnhancedCode: gosmtp.EnhancedCode{5, 7, 1},
					Message:      "rejected by sender domain DMARC policy",
				}
			case "quarantine":
				folder = "Junk"
				log.Printf("smtp: DMARC quarantine → Junk from=%s ip=%s", s.from, ip)
			}
		}
		// An unparseable From header (missing/malformed/multiple) means DMARC
		// evaluation itself was impossible — letting it pass in enforcement
		// mode would allow bypassing a reject policy with a malformed From
		// (fail-open). Quarantine to Junk as a suspicious signal (RFC 7489 §6.6.1).
		if s.backend.enforceDMARC && !vr.FromParsed {
			folder = "Junk"
			log.Printf("smtp: unparseable From header → quarantined to Junk from=%s ip=%s", s.from, ip)
		}
	}

	now := timeNow()
	delivered := 0
	for _, rc := range s.rcptList {
		// Prepend a per-recipient Received header (RFC 5321 §4.4 — delivery tracing)
		stamped := s.receivedHeader(rc.address, now)
		stamped = append(stamped, authHeader...)
		stamped = append(stamped, raw...)

		if err := s.deliver(rc, folder, stamped, now); err != nil {
			// Swallowing a partial delivery failure with 250 means the sending
			// server won't retry and the remaining recipients' mail is silently
			// lost — reject the whole transaction with 451 to induce a resend
			// (already-delivered recipients may get duplicates; at-least-once
			// beats loss).
			log.Printf("smtp: delivery failed to=%s from=%s (rejecting whole transaction with 451): %v", rc.address, s.from, err)
			return &gosmtp.SMTPError{
				Code:         451,
				EnhancedCode: gosmtp.EnhancedCode{4, 3, 0},
				Message:      "delivery failed, try again later",
			}
		}
		delivered++
	}

	log.Printf("smtp: delivery complete from=%s rcptList=%d/%d folder=%s size=%d", s.from, delivered, len(s.rcptList), folder, len(raw))
	return nil
}

// deliver stores the message in the recipient's given folder. Creates the folder if missing.
func (s *Session) deliver(rc rcpt, folder string, raw []byte, now time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()

	box, err := s.backend.store.GetMailbox(ctx, rc.user.ID, folder)
	if errors.Is(err, store.ErrNotFound) {
		box, err = s.backend.store.CreateMailbox(ctx, rc.user.ID, folder)
	}
	if err != nil {
		return fmt.Errorf("ensure %s: %w", folder, err)
	}

	// new mail has no flags (= unseen)
	_, err = s.backend.store.AppendMessage(ctx, box.ID, raw, nil, now)
	if err != nil {
		return fmt.Errorf("append: %w", err)
	}
	return nil
}

// receivedHeader builds an RFC 5321-style Received header.
func (s *Session) receivedHeader(forAddr string, now time.Time) []byte {
	helo := s.heloName
	if helo == "" {
		helo = "unknown"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Received: from %s (%s)\r\n", helo, s.remoteAddr)
	fmt.Fprintf(&b, "	by %s with ESMTP\r\n", s.backend.hostname)
	fmt.Fprintf(&b, "	for <%s>; %s\r\n", forAddr, now.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
	return []byte(b.String())
}

// remoteIP extracts the IP from a "1.2.3.4:5678"-style address.
func remoteIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	return net.ParseIP(host)
}

func (s *Session) Reset() {
	s.from = ""
	s.rcptList = nil
	s.suspicious = false
}

func (s *Session) Logout() error {
	return nil
}
