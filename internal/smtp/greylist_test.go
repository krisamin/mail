package smtp

import (
	"context"
	"errors"
	"testing"
	"time"

	gosmtp "github.com/emersion/go-smtp"
)

// Greylisting behavior tests — drives Session.Rcpt directly against the dev
// DB (setupServers seeds maro@krisam.in). Public-looking source IPs are
// simulated via remoteAddr; loopback traffic is exempt by design.

func TestGreylistRcpt(t *testing.T) {
	env := setupServers(t)
	_, _ = env.store.Pool().Exec(context.Background(), `TRUNCATE greylist`)

	b := NewBackend(env.store, "mx.test").WithGreylist(time.Minute)

	// 1) first contact from a public IP without FCrDNS → 451
	s := &Session{backend: b, remoteAddr: "203.0.113.9:33333", heloName: "x"}
	if err := s.Mail("stranger@example.org", &gosmtp.MailOptions{}); err != nil {
		t.Fatalf("mail: %v", err)
	}
	err := s.Rcpt(testAddr, &gosmtp.RcptOptions{})
	var serr *gosmtp.SMTPError
	if !errors.As(err, &serr) || serr.Code != 451 {
		t.Fatalf("first contact should get 451: %v", err)
	}
	t.Log("✔ first contact 451")

	// 2) same triplet from a sibling host in the /24 — still one triplet,
	// and passes once the delay has elapsed (backdate first_seen)
	if _, err := env.store.Pool().Exec(context.Background(),
		`UPDATE greylist SET first_seen = now() - interval '5 minutes'`); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	s2 := &Session{backend: b, remoteAddr: "203.0.113.77:44444", heloName: "x"}
	if err := s2.Mail("stranger@example.org", &gosmtp.MailOptions{}); err != nil {
		t.Fatalf("mail: %v", err)
	}
	if err := s2.Rcpt(testAddr, &gosmtp.RcptOptions{}); err != nil {
		t.Fatalf("post-delay retry from sibling host must pass: %v", err)
	}
	t.Log("✔ /24-keyed retry passes after delay")

	// 3) FCrDNS-verified sessions bypass greylisting entirely
	_, _ = env.store.Pool().Exec(context.Background(), `TRUNCATE greylist`)
	s3 := &Session{backend: b, remoteAddr: "198.51.100.5:5555", heloName: "mail.legit.example", rdnsVerified: true}
	if err := s3.Mail("legit@example.net", &gosmtp.MailOptions{}); err != nil {
		t.Fatalf("mail: %v", err)
	}
	if err := s3.Rcpt(testAddr, &gosmtp.RcptOptions{}); err != nil {
		t.Fatalf("FCrDNS-verified sender must skip greylist: %v", err)
	}
	t.Log("✔ FCrDNS-verified exempt")

	// 4) loopback/dev traffic exempt
	s4 := &Session{backend: b, remoteAddr: "127.0.0.1:2525", heloName: "dev"}
	if err := s4.Mail("dev@example.org", &gosmtp.MailOptions{}); err != nil {
		t.Fatalf("mail: %v", err)
	}
	if err := s4.Rcpt(testAddr, &gosmtp.RcptOptions{}); err != nil {
		t.Fatalf("loopback must skip greylist: %v", err)
	}
	t.Log("✔ loopback exempt")

	// 5) unknown recipient keeps its clean 550 (greylist runs after validation)
	s5 := &Session{backend: b, remoteAddr: "203.0.113.9:33333", heloName: "x"}
	if err := s5.Mail("stranger@example.org", &gosmtp.MailOptions{}); err != nil {
		t.Fatalf("mail: %v", err)
	}
	err = s5.Rcpt("nobody@krisam.in", &gosmtp.RcptOptions{})
	if !errors.As(err, &serr) || serr.Code != 550 {
		t.Fatalf("unknown rcpt should stay 550: %v", err)
	}
	t.Log("✔ unknown recipient still 550")
}
