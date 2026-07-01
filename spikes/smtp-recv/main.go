// Phase 0 스파이크: go-smtp 수신 서버
//
// 목적: SMTP가 "그냥 텍스트 상태머신"이라는 걸 몸으로 이해한다.
// 클라이언트(swaks 등)가 보내는 EHLO -> MAIL FROM -> RCPT TO -> DATA
// 흐름을 각 콜백에서 로그로 관찰하고, 받은 메일 본문을 go-message로
// 파싱해서 헤더/본문을 뜯어본다.
//
// 저장 안 함. 인증 안 함. 오직 프로토콜 흐름 관찰용 (버리는 코드).
//
// 실행:
//   go run ./spikes/smtp-recv
// 다른 터미널에서:
//   swaks --to test@localhost --from me@example.com \
//         --server localhost:2525 --body "hello shiro"
package main

import (
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-smtp"
)

// ─────────────────────────────────────────────────────────────
// Backend: 서버 전체. 새 연결마다 NewSession이 불린다.
// ─────────────────────────────────────────────────────────────
type Backend struct{}

func (b *Backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	log.Printf("┌─ 새 연결: %s", c.Conn().RemoteAddr())
	return &Session{}, nil
}

// ─────────────────────────────────────────────────────────────
// Session: 하나의 SMTP 대화(트랜잭션). 상태머신의 상태를 담는다.
// go-smtp가 프로토콜 파싱을 다 해주고, 우리는 각 명령의 콜백만 채운다.
// ─────────────────────────────────────────────────────────────
type Session struct {
	from string
	to   []string
}

// MAIL FROM:<...> 이 도착하면 호출
func (s *Session) Mail(from string, opts *smtp.MailOptions) error {
	log.Printf("│  MAIL FROM: %s", from)
	s.from = from
	return nil
}

// RCPT TO:<...> 가 도착하면 호출 (수신자 여러 명이면 여러 번)
func (s *Session) Rcpt(to string, opts *smtp.RcptOptions) error {
	log.Printf("│  RCPT TO:   %s", to)
	s.to = append(s.to, to)
	return nil
}

// DATA 이후 실제 메일 본문(raw)이 r로 흘러들어온다.
func (s *Session) Data(r io.Reader) error {
	log.Printf("│  DATA 수신 시작...")

	// 1) raw 전체를 읽어둔다 (스파이크니까 통째로)
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	log.Printf("│  raw 크기: %d bytes", len(raw))

	// 2) go-message/mail 로 파싱 — MIME, 헤더, 멀티파트를 다뤄준다.
	mr, err := mail.CreateReader(strings.NewReader(string(raw)))
	if err != nil {
		log.Printf("│  ⚠ 파싱 실패(그래도 raw는 받음): %v", err)
		dumpRaw(raw)
		return nil
	}

	h := mr.Header
	if date, err := h.Date(); err == nil {
		log.Printf("│  Date:    %s", date.Format(time.RFC3339))
	}
	if subj, err := h.Subject(); err == nil {
		log.Printf("│  Subject: %s", subj)
	}
	if fromList, err := h.AddressList("From"); err == nil {
		log.Printf("│  From:    %v", fromList)
	}
	if toList, err := h.AddressList("To"); err == nil {
		log.Printf("│  To:      %v", toList)
	}

	// 3) 각 파트(본문/첨부)를 순회
	partN := 0
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("│  ⚠ 파트 읽기 오류: %v", err)
			break
		}
		partN++
		switch hdr := p.Header.(type) {
		case *mail.InlineHeader:
			ct, _, _ := hdr.ContentType()
			body, _ := io.ReadAll(p.Body)
			log.Printf("│  [파트 %d] 본문 (%s):", partN, ct)
			for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
				log.Printf("│      %s", line)
			}
		case *mail.AttachmentHeader:
			fn, _ := hdr.Filename()
			log.Printf("│  [파트 %d] 첨부: %s", partN, fn)
		}
	}

	log.Printf("│  ✔ 처리 완료 (from=%s, to=%v)", s.from, s.to)
	return nil
}

func (s *Session) Reset() {
	log.Printf("│  RESET")
	s.from = ""
	s.to = nil
}

func (s *Session) Logout() error {
	log.Printf("└─ 연결 종료")
	return nil
}

func dumpRaw(raw []byte) {
	fmt.Println("── RAW ──")
	fmt.Println(string(raw))
	fmt.Println("─────────")
}

func main() {
	be := &Backend{}
	s := smtp.NewServer(be)

	s.Addr = ":2525" // 25는 권한 필요 → 스파이크는 2525
	s.Domain = "localhost"
	s.WriteTimeout = 10 * time.Second
	s.ReadTimeout = 10 * time.Second
	s.MaxMessageBytes = 10 * 1024 * 1024 // 10MB
	s.MaxRecipients = 50
	s.AllowInsecureAuth = true // 스파이크: TLS 없이 평문 허용

	log.Println("🥛 mail 스파이크 SMTP 수신 서버 — localhost:2525")
	log.Println("   테스트: swaks --to test@localhost --from me@example.com --server localhost:2525 --body \"hello shiro\"")
	if err := s.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
