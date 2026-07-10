// Package smtp는 store.Store 위에서 go-smtp 수신 백엔드를 구현한다.
//
// Phase 2-1: 외부에서 들어오는 메일(port 25 MX 역할)을 받아 수신자 검증 후
// INBOX에 배달한다. 인증 없음 — MX 수신은 원래 익명이다 (발신자 검증은
// Phase 2-4의 SPF/DKIM/DMARC 몫). submission(587, AUTH 필수)은 별도.
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
	"github.com/krisamin/mail/internal/store"
)

// opTimeout은 SMTP 콜백 하나가 store에 접근할 때의 상한.
const opTimeout = 30 * time.Second

// timeNow는 테스트에서 바꿔치기 가능하도록 변수로.
var timeNow = time.Now

// Backend는 수신(MX) SMTP 백엔드.
type Backend struct {
	store    store.Store
	hostname string // Received 헤더에 박을 서버 이름
	// verifyInbound가 true면 수신 메일에 SPF/DKIM/DMARC 검증을 돌려
	// Authentication-Results 헤더를 붙인다 (기록).
	verifyInbound bool
	// enforceDMARC가 true면 발신 도메인의 DMARC 정책을 집행한다:
	// 판정 fail + p=reject → 550 거절, p=quarantine → Junk 폴더 배달.
	// p=none이거나 레코드 없으면 기록만 (기존 동작).
	enforceDMARC bool
}

// NewBackend는 store 위에 수신 백엔드를 만든다.
func NewBackend(st store.Store, hostname string) *Backend {
	return &Backend{store: st, hostname: hostname}
}

// WithInboundVerification은 수신 SPF/DKIM/DMARC 검증을 켠다.
func (b *Backend) WithInboundVerification() *Backend {
	b.verifyInbound = true
	return b
}

// WithDMARCEnforcement는 DMARC 정책 집행을 켠다 (검증도 함께 켜짐).
func (b *Backend) WithDMARCEnforcement() *Backend {
	b.verifyInbound = true
	b.enforceDMARC = true
	return b
}

// NewSession은 연결마다 불린다 (gosmtp.Backend 인터페이스).
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

// rcpt는 검증 통과한 수신자 하나.
type rcpt struct {
	address string
	user    *store.Account
}

// Session은 SMTP 트랜잭션 하나 (gosmtp.Session 구현).
type Session struct {
	backend    *Backend
	remoteAddr string
	heloName   string

	from     string
	rcptList []rcpt
}

var _ gosmtp.Session = (*Session)(nil)

func (s *Session) Mail(from string, opts *gosmtp.MailOptions) error {
	s.from = from
	return nil
}

// Rcpt는 수신자가 우리 유저인지 검증한다. 아니면 550 5.1.1.
// ★여기서 거절해야 backscatter(수락 후 반송 스팸)를 안 만든다.
// 별칭·와일드카드도 배달 대상 (ResolveAddress).
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

// Data는 본문을 받아 각 수신자의 INBOX에 배달한다.
// DMARC 집행이 켜져 있으면: fail+p=reject → 550 거절,
// fail+p=quarantine → Junk 폴더 배달.
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

	// SPF/DKIM/DMARC 검증 → Authentication-Results 기록 + 정책 판단
	var authHeader []byte
	folder := "INBOX"
	if s.backend.verifyInbound {
		ip := remoteIP(s.remoteAddr)
		vr := auth.VerifyInbound(raw, auth.VerifyOptions{
			RemoteIP:     ip,
			HeloName:     s.heloName,
			EnvelopeFrom: s.from,
			Hostname:     s.backend.hostname,
		})
		authHeader = vr.Header
		log.Printf("smtp: 인증 검증 from=%s ip=%s spf=%v dkim=%v dmarc=%v policy=%s",
			s.from, ip, vr.SPFPass, vr.DKIMPass, vr.DMARCPass, vr.DMARCPolicy)

		// DMARC 정책 집행 — 발신 도메인이 공표한 정책을 따른다.
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
	}

	now := timeNow()
	delivered := 0
	for _, rc := range s.rcptList {
		// 수신자별 Received 헤더 prepend (RFC 5321 §4.4 — 배달 추적용)
		stamped := s.receivedHeader(rc.address, now)
		stamped = append(stamped, authHeader...)
		stamped = append(stamped, raw...)

		if err := s.deliver(rc, folder, stamped, now); err != nil {
			log.Printf("smtp: 배달 실패 to=%s from=%s: %v", rc.address, s.from, err)
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
	log.Printf("smtp: 배달 완료 from=%s rcptList=%d/%d folder=%s size=%d", s.from, delivered, len(s.rcptList), folder, len(raw))
	return nil
}

// deliver는 수신자의 지정 폴더에 메시지를 저장한다. 폴더 없으면 생성.
func (s *Session) deliver(rc rcpt, folder string, raw []byte, now time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()

	box, err := s.backend.store.GetMailbox(ctx, rc.user.ID, folder)
	if errors.Is(err, store.ErrNotFound) {
		box, err = s.backend.store.CreateMailbox(ctx, rc.user.ID, folder)
	}
	if err != nil {
		return fmt.Errorf("%s 확보: %w", folder, err)
	}

	// 새 메일은 플래그 없음 (= unseen)
	_, err = s.backend.store.AppendMessage(ctx, box.ID, raw, nil, now)
	if err != nil {
		return fmt.Errorf("append: %w", err)
	}
	return nil
}

// receivedHeader는 RFC 5321 형식의 Received 헤더를 만든다.
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

// remoteIP는 "1.2.3.4:5678" 형태에서 IP를 뽑는다.
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
}

func (s *Session) Logout() error {
	return nil
}
