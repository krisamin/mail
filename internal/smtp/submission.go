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

	"github.com/krisamin/mail/internal/guard"
	"github.com/krisamin/mail/internal/store"
)

// SubmissionBackend는 메일 제출(submission, 587 역할) 백엔드.
// 수신(MX)과 반대로 **AUTH가 필수**다 — 우리 유저가 앱 비밀번호로 인증하고
// 메일을 내보내는 문. DD-02: 메일 앱 인증 = 앱 비밀번호.
//
// 라우팅: 로컬 도메인 수신자는 직접 배달, 외부 도메인은 발송 큐로
// (Phase 2-3). 큐가 없으면(EnqueueDisabled) 외부 도메인은 550 거절.
type SubmissionBackend struct {
	store    store.Store
	hostname string
	// enqueueEnabled가 false면 외부 도메인 수신자를 거절한다
	// (발송 큐 워커가 안 도는 구성 — dev에서 relay 미설정일 때).
	enqueueEnabled bool
	// limiter는 인증 브루트포스 방어 (IP 단위).
	limiter *guard.Limiter
}

// NewSubmissionBackend는 submission 백엔드를 만든다.
// enqueueEnabled: 외부 도메인 수신자를 발송 큐에 넣을지 여부.
func NewSubmissionBackend(st store.Store, hostname string, enqueueEnabled bool) *SubmissionBackend {
	return &SubmissionBackend{
		store: st, hostname: hostname, enqueueEnabled: enqueueEnabled,
		limiter: guard.NewLimiter(),
	}
}

// NewSession은 연결마다 불린다.
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

// SubmissionSession은 인증된 제출 트랜잭션.
type SubmissionSession struct {
	backend    *SubmissionBackend
	remoteAddr string
	heloName   string

	user        *store.Account // 인증 성공 시 채워짐
	accountAddr string         // 인증에 쓴 주소 (envelope from 검증용)

	from     string
	rcptList []rcpt   // 로컬 배달 대상
	external []string // 외부 도메인 → 발송 큐 대상
}

var _ gosmtp.Session = (*SubmissionSession)(nil)
var _ gosmtp.AuthSession = (*SubmissionSession)(nil)

// AuthMechanisms는 지원 SASL 메커니즘 목록.
func (s *SubmissionSession) AuthMechanisms() []string {
	return []string{sasl.Plain}
}

// Auth는 SASL 서버를 돌려준다. PLAIN = 주소 + 앱 비밀번호.
// IP 단위 브루트포스 방어: 반복 실패 시 일정 시간 차단.
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
			return &gosmtp.SMTPError{
				Code:         421,
				EnhancedCode: gosmtp.EnhancedCode{4, 7, 0},
				Message:      "too many failed attempts, try again later",
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()

		u, err := s.backend.store.AuthenticateAppPassword(ctx, username, password)
		if err != nil {
			if errors.Is(err, store.ErrAuthFailed) || errors.Is(err, store.ErrNotFound) {
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
		s.backend.limiter.Success(ipKey)
		s.backend.limiter.Success(acctKey)
		s.user = u
		s.accountAddr = strings.ToLower(username)
		return nil
	}), nil
}

// Mail은 AUTH 필수 + envelope from이 인증 계정 소유 주소여야 한다
// (발신자 위조 방지). 본인 주소 또는 본인에게 걸린 별칭(와일드카드 포함).
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
	return nil
}

// Rcpt는 수신자를 분류한다: 로컬 도메인이면 유저 확인, 외부면 Phase 2-3 전까지 거절.
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
			// 외부 도메인 → 발송 큐 (활성화된 경우만)
			if !s.backend.enqueueEnabled {
				return &gosmtp.SMTPError{
					Code:         550,
					EnhancedCode: gosmtp.EnhancedCode{5, 7, 1},
					Message:      "relaying to external domainList is disabled",
				}
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
	s.rcptList = append(s.rcptList, rcpt{address: to, user: u})
	return nil
}

// Data는 본문을 받아 로컬 수신자에게 배달하고, 외부 수신자는 발송 큐에 넣는다.
//
// 실패 정책: 한 건이라도 배달/큐 삽입에 실패하면 451로 트랜잭션 전체를
// 거절한다 — 250을 돌려주고 일부를 조용히 유실하는 것보다, 클라이언트
// 재전송으로 인한 중복 배달(at-least-once)이 낫다.
func (s *SubmissionSession) Data(r io.Reader) error {
	if len(s.rcptList) == 0 && len(s.external) == 0 {
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

	// 헤더 From 검증 — envelope(Mail)만 믿으면 헤더 From:에 타인 주소를
	// 박아 보낼 수 있고, 큐 워커가 도메인 키로 DKIM 서명까지 얹어준다.
	// 헤더 From도 인증 계정 소유 주소여야 한다 (스푸핑 차단).
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

	// 배달 로직은 수신 세션과 공유
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
			log.Printf("submission: 배달 실패 to=%s from=%s: %v", rc.address, s.from, err)
			return &gosmtp.SMTPError{
				Code:         451,
				EnhancedCode: gosmtp.EnhancedCode{4, 3, 0},
				Message:      "delivery failed, try again later",
			}
		}
		delivered++
	}

	// 외부 수신자 → 발송 큐 (Received 헤더는 서버 통과 증적으로 동일하게 찍음)
	enqueued := 0
	if len(s.external) > 0 {
		stamped := inbound.receivedHeader(s.external[0], now)
		stamped = append(stamped, raw...)
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		err := s.backend.store.EnqueueOutbound(ctx, s.from, s.external, stamped)
		cancel()
		if err != nil {
			// 큐 삽입 실패를 250으로 삼키면 클라이언트는 "보냈다"고 믿는데
			// 외부 수신자는 영원히 못 받는다 — 451로 재시도 유도.
			log.Printf("submission: 큐 삽입 실패 from=%s rcptList=%v: %v", s.from, s.external, err)
			return &gosmtp.SMTPError{
				Code:         451,
				EnhancedCode: gosmtp.EnhancedCode{4, 3, 0},
				Message:      "queueing failed, try again later",
			}
		}
		enqueued = len(s.external)
	}

	log.Printf("submission: 제출 완료 user=%s local=%d/%d queued=%d/%d size=%d",
		s.accountAddr, delivered, len(s.rcptList), enqueued, len(s.external), len(raw))
	return nil
}

func (s *SubmissionSession) Reset() {
	s.from = ""
	s.rcptList = nil
	s.external = nil
}

// headerFromAddress는 메시지 헤더 블록의 From: 주소를 소문자로 뽑는다.
// 파싱 실패/부재 시 빈 문자열 (호출부에서 검증 스킵 — envelope 검증은
// Mail 단계에서 이미 통과한 상태).
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
