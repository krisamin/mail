package queue

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/krisamin/mail/internal/store"
)

// bounce DSN (RFC 3464) — 영구 실패한 발송을 발신자에게 알린다.
//
// 발신자는 항상 우리 서버의 로컬 유저이므로 (submission만 큐에 넣는다)
// SMTP 왕복 없이 발신자의 INBOX에 직접 Append한다.
//
// 루프 방지 (RFC 5321 §4.5.5):
//   - envelope from이 빈 값(<>)인 메시지(=DSN 자신)에는 DSN을 만들지 않는다.
//   - DSN 자체의 envelope from은 개념상 <>이지만 우리는 로컬 Append라
//     envelope 없이 저장 — Return-Path 헤더로 <>를 명시한다.

// buildDSN은 multipart/report(delivery-status) 메시지를 만든다.
func buildDSN(hostname string, m *store.OutboundMessage, reason string, now time.Time) []byte {
	boundary := fmt.Sprintf("dsn-%d-%d", m.ID, now.Unix())
	date := now.UTC().Format(time.RFC1123Z)

	// 원문 헤더만 발췌 (message/rfc822-headers — 본문 전체를 되돌려보내면
	// 대용량 메일 바운스가 스토리지를 이중으로 먹는다)
	headerEnd := strings.Index(string(m.Raw), "\r\n\r\n")
	origHeader := string(m.Raw)
	if headerEnd >= 0 {
		origHeader = string(m.Raw[:headerEnd+2])
	}

	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	w("Return-Path: <>\r\n")
	w("From: Mail Delivery Subsystem <mailer-daemon@%s>\r\n", hostname)
	w("To: <%s>\r\n", m.EnvelopeFrom)
	w("Subject: Undelivered Mail Returned to Sender\r\n")
	w("Date: %s\r\n", date)
	w("Auto-Submitted: auto-replied\r\n")
	w("MIME-Version: 1.0\r\n")
	w("Content-Type: multipart/report; report-type=delivery-status; boundary=%q\r\n", boundary)
	w("\r\n")

	// part 1 — human readable
	w("--%s\r\n", boundary)
	w("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	w("Your message to <%s> could not be delivered.\r\n\r\n", m.EnvelopeRcpt)
	w("Reason: %s\r\n\r\n", reason)
	w("This is a permanent error. No further attempts will be made.\r\n")
	w("\r\n")

	// part 2 — machine readable delivery-status
	w("--%s\r\n", boundary)
	w("Content-Type: message/delivery-status\r\n\r\n")
	w("Reporting-MTA: dns; %s\r\n\r\n", hostname)
	w("Final-Recipient: rfc822; %s\r\n", m.EnvelopeRcpt)
	w("Action: failed\r\n")
	w("Status: 5.0.0\r\n")
	w("Diagnostic-Code: smtp; %s\r\n", strings.ReplaceAll(reason, "\n", " "))
	w("\r\n")

	// part 3 — original headers
	w("--%s\r\n", boundary)
	w("Content-Type: message/rfc822-headers\r\n\r\n")
	w("%s", origHeader)
	w("\r\n--%s--\r\n", boundary)

	return []byte(b.String())
}

// deliverDSN은 발신자(로컬 유저)의 INBOX에 DSN을 넣는다.
func (w *Worker) deliverDSN(ctx context.Context, m *store.OutboundMessage, reason string) {
	// 루프 방지: DSN의 DSN은 만들지 않는다.
	if m.EnvelopeFrom == "" || m.EnvelopeFrom == "<>" {
		return
	}
	sender, err := w.store.ResolveAddress(ctx, strings.ToLower(m.EnvelopeFrom))
	if err != nil {
		log.Printf("queue: DSN 발신자 해석 실패 from=%s: %v", m.EnvelopeFrom, err)
		return
	}
	mbox, err := w.store.GetMailbox(ctx, sender.ID, "INBOX")
	if err != nil {
		log.Printf("queue: DSN INBOX 조회 실패 account=%d: %v", sender.ID, err)
		return
	}
	dsn := buildDSN(w.hostname, m, reason, time.Now())
	if _, err := w.store.AppendMessage(ctx, mbox.ID, dsn, nil, time.Now()); err != nil {
		log.Printf("queue: DSN 배달 실패 account=%d: %v", sender.ID, err)
		return
	}
	log.Printf("queue: DSN 배달 완료 to=%s (원 수신자 %s)", m.EnvelopeFrom, m.EnvelopeRcpt)
}
