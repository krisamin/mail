package smtp

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"

	"github.com/krisamin/mail/internal/store"
)

// SubmissionBackend는 메일 제출(submission, 587 역할) 백엔드.
// 수신(MX)과 반대로 **AUTH가 필수**다 — 우리 유저가 앱 비밀번호로 인증하고
// 메일을 내보내는 문. DD-02: 메일 앱 인증 = 앱 비밀번호.
//
// Phase 2-2 범위: 로컬 도메인 수신자에게 직접 배달.
// 외부 도메인 발송은 Phase 2-3 발송 큐가 붙기 전까지 명시적으로 거절한다.
type SubmissionBackend struct {
	store    store.Store
	hostname string
}

// NewSubmissionBackend는 submission 백엔드를 만든다.
func NewSubmissionBackend(st store.Store, hostname string) *SubmissionBackend {
	return &SubmissionBackend{store: st, hostname: hostname}
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

	user     *store.User // 인증 성공 시 채워짐
	userAddr string      // 인증에 쓴 주소 (envelope from 검증용)

	from  string
	rcpts []rcpt
}

var _ gosmtp.Session = (*SubmissionSession)(nil)
var _ gosmtp.AuthSession = (*SubmissionSession)(nil)

// AuthMechanisms는 지원 SASL 메커니즘 목록.
func (s *SubmissionSession) AuthMechanisms() []string {
	return []string{sasl.Plain}
}

// Auth는 SASL 서버를 돌려준다. PLAIN = 주소 + 앱 비밀번호.
func (s *SubmissionSession) Auth(mech string) (sasl.Server, error) {
	if mech != sasl.Plain {
		return nil, gosmtp.ErrAuthUnsupported
	}
	return sasl.NewPlainServer(func(identity, username, password string) error {
		if identity != "" && identity != username {
			return errors.New("identity not supported")
		}
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()

		u, err := s.backend.store.AuthenticateAppPassword(ctx, username, password)
		if err != nil {
			if errors.Is(err, store.ErrAuthFailed) || errors.Is(err, store.ErrNotFound) {
				return &gosmtp.SMTPError{
					Code:         535,
					EnhancedCode: gosmtp.EnhancedCode{5, 7, 8},
					Message:      "authentication failed",
				}
			}
			return err
		}
		s.user = u
		s.userAddr = strings.ToLower(username)
		return nil
	}), nil
}

// Mail은 AUTH 필수 + envelope from이 인증 계정과 일치해야 한다 (발신자 위조 방지).
func (s *SubmissionSession) Mail(from string, opts *gosmtp.MailOptions) error {
	if s.user == nil {
		return gosmtp.ErrAuthRequired
	}
	if strings.ToLower(from) != s.userAddr {
		return &gosmtp.SMTPError{
			Code:         553,
			EnhancedCode: gosmtp.EnhancedCode{5, 7, 1},
			Message:      "sender address must match authenticated user",
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
			// 외부 도메인 → 발송 큐(Phase 2-3)가 붙기 전까진 정직하게 거절
			return &gosmtp.SMTPError{
				Code:         550,
				EnhancedCode: gosmtp.EnhancedCode{5, 7, 1},
				Message:      "relaying to external domains not yet supported",
			}
		}
		return err
	}

	u, err := s.backend.store.FindUserByAddress(ctx, to)
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
	s.rcpts = append(s.rcpts, rcpt{address: to, user: u})
	return nil
}

// Data는 본문을 받아 로컬 수신자에게 배달한다.
// (수신 경로와 같은 배달 로직 재사용 — Session.deliver)
func (s *SubmissionSession) Data(r io.Reader) error {
	if len(s.rcpts) == 0 {
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

	// 배달 로직은 수신 세션과 공유
	inbound := &Session{
		backend:    &Backend{store: s.backend.store, hostname: s.backend.hostname},
		remoteAddr: s.remoteAddr,
		heloName:   s.heloName,
		from:       s.from,
	}

	now := timeNow()
	delivered := 0
	for _, rc := range s.rcpts {
		stamped := inbound.receivedHeader(rc.address, now)
		stamped = append(stamped, raw...)
		if err := inbound.deliver(rc, stamped, now); err != nil {
			log.Printf("submission: 배달 실패 to=%s from=%s: %v", rc.address, s.from, err)
			continue
		}
		delivered++
	}
	if delivered == 0 {
		return &gosmtp.SMTPError{
			Code:         451,
			EnhancedCode: gosmtp.EnhancedCode{4, 3, 0},
			Message:      "delivery failed, try again later",
		}
	}
	log.Printf("submission: 제출 완료 user=%s rcpts=%d/%d size=%d", s.userAddr, delivered, len(s.rcpts), len(raw))
	return nil
}

func (s *SubmissionSession) Reset() {
	s.from = ""
	s.rcpts = nil
}

func (s *SubmissionSession) Logout() error {
	return nil
}
