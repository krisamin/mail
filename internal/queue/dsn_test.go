package queue

import (
	"strings"
	"testing"
	"time"

	"github.com/krisamin/mail/internal/store"
)

func TestBuildDSN(t *testing.T) {
	m := &store.OutboundMessage{
		ID:           42,
		EnvelopeFrom: "maro@kirby.so",
		EnvelopeRcpt: "nobody@example.com",
		Raw: []byte("From: maro@kirby.so\r\nTo: nobody@example.com\r\nSubject: hello\r\n\r\n" +
			"body body body\r\n"),
	}
	dsn := string(buildDSN("mail.krisam.in", m, "550 5.1.1 user unknown", time.Unix(1700000000, 0)))

	for _, want := range []string{
		"Return-Path: <>",
		"From: Mail Delivery Subsystem <mailer-daemon@mail.krisam.in>",
		"To: <maro@kirby.so>",
		"Auto-Submitted: auto-replied",
		"report-type=delivery-status",
		"Final-Recipient: rfc822; nobody@example.com",
		"Action: failed",
		"Diagnostic-Code: smtp; 550 5.1.1 user unknown",
		"Content-Type: message/rfc822-headers",
		"Subject: hello", // original header excerpt
	} {
		if !strings.Contains(dsn, want) {
			t.Errorf("DSN missing %q\n%s", want, dsn)
		}
	}
	// the original body is not included (headers only)
	if strings.Contains(dsn, "body body body") {
		t.Error("DSN must not include the original body (rfc822-headers only)")
	}
}
