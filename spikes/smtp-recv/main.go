// Phase 0 spike: go-smtp receiving server
//
// Purpose: internalize that SMTP is "just a text state machine".
// Observe the EHLO -> MAIL FROM -> RCPT TO -> DATA flow a client
// (swaks etc.) sends by logging each callback, and dissect the received
// mail body's headers/parts by parsing it with go-message.
//
// No storage. No auth. Protocol-flow observation only (throwaway code).
//
// Run:
//
//	go run ./spikes/smtp-recv
//
// In another terminal:
//
//	swaks --to test@localhost --from me@example.com \
//	      --server localhost:2525 --body "hello shiro"
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
// Backend: the whole server. NewSession is called per connection.
// ─────────────────────────────────────────────────────────────
type Backend struct{}

func (b *Backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	log.Printf("┌─ new connection: %s", c.Conn().RemoteAddr())
	return &Session{}, nil
}

// ─────────────────────────────────────────────────────────────
// Session: one SMTP conversation (transaction). Holds the state machine's state.
// go-smtp does all the protocol parsing; we only fill each command's callback.
// ─────────────────────────────────────────────────────────────
type Session struct {
	from string
	to   []string
}

// called when MAIL FROM:<...> arrives
func (s *Session) Mail(from string, opts *smtp.MailOptions) error {
	log.Printf("│  MAIL FROM: %s", from)
	s.from = from
	return nil
}

// called when RCPT TO:<...> arrives (multiple times for multiple recipients)
func (s *Session) Rcpt(to string, opts *smtp.RcptOptions) error {
	log.Printf("│  RCPT TO:   %s", to)
	s.to = append(s.to, to)
	return nil
}

// after DATA, the actual raw mail body streams in via r.
func (s *Session) Data(r io.Reader) error {
	log.Printf("│  DATA receiving...")

	// 1) read the entire raw body (it's a spike — whole thing)
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	log.Printf("│  raw size: %d bytes", len(raw))

	// 2) parse with go-message/mail — handles MIME, headers, multipart.
	mr, err := mail.CreateReader(strings.NewReader(string(raw)))
	if err != nil {
		log.Printf("│  ⚠ parse failed (raw still received): %v", err)
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

	// 3) iterate the parts (body/attachments)
	partN := 0
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("│  ⚠ part read error: %v", err)
			break
		}
		partN++
		switch hdr := p.Header.(type) {
		case *mail.InlineHeader:
			ct, _, _ := hdr.ContentType()
			body, _ := io.ReadAll(p.Body)
			log.Printf("│  [part %d] body (%s):", partN, ct)
			for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
				log.Printf("│      %s", line)
			}
		case *mail.AttachmentHeader:
			fn, _ := hdr.Filename()
			log.Printf("│  [part %d] attachment: %s", partN, fn)
		}
	}

	log.Printf("│  ✔ done (from=%s, to=%v)", s.from, s.to)
	return nil
}

func (s *Session) Reset() {
	log.Printf("│  RESET")
	s.from = ""
	s.to = nil
}

func (s *Session) Logout() error {
	log.Printf("└─ connection closed")
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

	s.Addr = ":2525" // port 25 needs privileges → spike uses 2525
	s.Domain = "localhost"
	s.WriteTimeout = 10 * time.Second
	s.ReadTimeout = 10 * time.Second
	s.MaxMessageBytes = 10 * 1024 * 1024 // 10MB
	s.MaxRecipients = 50
	s.AllowInsecureAuth = true // spike: plaintext without TLS allowed

	log.Println("🥛 mail spike SMTP receiving server — localhost:2525")
	log.Println("   test: swaks --to test@localhost --from me@example.com --server localhost:2525 --body \"hello shiro\"")
	if err := s.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
