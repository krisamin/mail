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
		"Subject: hello", // 원문 헤더 발췌
	} {
		if !strings.Contains(dsn, want) {
			t.Errorf("DSN에 %q 없음\n%s", want, dsn)
		}
	}
	// 원문 본문은 포함하지 않는다 (헤더만)
	if strings.Contains(dsn, "body body body") {
		t.Error("DSN이 원문 본문을 포함하면 안 됨 (rfc822-headers만)")
	}
}
