package queue

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/krisamin/mail/internal/delivery"
	"github.com/krisamin/mail/internal/store"
)

// bounce DSN (RFC 3464) — notifies the sender of a permanently failed send.
//
// The sender is always a local user of our server (only submission enqueues),
// so the DSN is Appended directly to the sender's INBOX without an SMTP
// round trip.
//
// Loop prevention (RFC 5321 §4.5.5):
//   - No DSN is generated for messages whose envelope from is empty (<>)
//     (i.e. a DSN itself).
//   - The DSN's own envelope from is conceptually <>, but we store it via a
//     local Append with no envelope — the Return-Path header states <> explicitly.

// buildDSN builds a multipart/report (delivery-status) message.
func buildDSN(hostname string, m *store.OutboundMessage, reason string, now time.Time) []byte {
	boundary := fmt.Sprintf("dsn-%d-%d", m.ID, now.Unix())
	date := now.UTC().Format(time.RFC1123Z)

	// excerpt only the original headers (message/rfc822-headers — returning
	// the whole body would make bounces of large mails eat storage twice)
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

// deliverDSN puts the DSN into the sender's (a local user's) INBOX.
func (w *Worker) deliverDSN(ctx context.Context, m *store.OutboundMessage, reason string) {
	// loop prevention: never generate a DSN for a DSN.
	if m.EnvelopeFrom == "" || m.EnvelopeFrom == "<>" {
		return
	}
	sender, err := w.store.ResolveAddress(ctx, strings.ToLower(m.EnvelopeFrom))
	if err != nil {
		log.Printf("queue: DSN sender resolve failed from=%s: %v", m.EnvelopeFrom, err)
		return
	}
	dsn := buildDSN(w.hostname, m, reason, time.Now())
	// shared local pipeline — the sender's own filter rules apply to bounces too
	if _, err := delivery.Deliver(ctx, w.store, delivery.Request{
		AccountID: sender.ID,
		Address:   m.EnvelopeFrom,
		Origin:    "queue",
		Raw:       dsn,
	}); err != nil {
		log.Printf("queue: DSN delivery failed account=%s: %v", sender.ID, err)
		return
	}
	log.Printf("queue: DSN delivered to=%s (original recipient %s)", m.EnvelopeFrom, m.EnvelopeRcpt)
}
