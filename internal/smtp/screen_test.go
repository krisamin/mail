package smtp

import (
	"context"
	"errors"
	"testing"

	gosmtp "github.com/emersion/go-smtp"

	"github.com/krisamin/mail/internal/spam"
)

// Connection-screening unit tests — no DB needed. Builds a Session directly
// and drives Mail() against a Checker with injected DNS lookups.

func fakeChecker(t *testing.T) *spam.Checker {
	t.Helper()
	c := spam.NewChecker([]string{"zen.example.test"})
	spam.SetLookupsForTest(c,
		func(_ context.Context, host string) ([]string, error) {
			switch host {
			case "4.3.2.1.zen.example.test":
				return []string{"127.0.0.4"}, nil // 1.2.3.4 listed
			case "mail.good.example":
				return []string{"1.2.3.9"}, nil
			default:
				return nil, errors.New("NXDOMAIN")
			}
		},
		func(_ context.Context, addr string) ([]string, error) {
			if addr == "1.2.3.9" {
				return []string{"mail.good.example."}, nil
			}
			return nil, errors.New("no PTR")
		},
	)
	return c
}

func TestMailFromDNSBLReject(t *testing.T) {
	b := NewBackend(nil, "mx.test").WithSpamChecker(fakeChecker(t))
	s := &Session{backend: b, remoteAddr: "1.2.3.4:2525", heloName: "mail.good.example"}

	err := s.Mail("spammer@example.com", &gosmtp.MailOptions{})
	var serr *gosmtp.SMTPError
	if !errors.As(err, &serr) || serr.Code != 554 {
		t.Fatalf("DNSBL-listed IP should get 554: %v", err)
	}
	t.Log("✔ DNSBL listed → 554 at MAIL FROM")
}

func TestMailFromSuspiciousQuarantineFlag(t *testing.T) {
	b := NewBackend(nil, "mx.test").WithSpamChecker(fakeChecker(t))

	// no PTR + bare-word HELO → suspicious (quarantine flag), NOT rejected
	s := &Session{backend: b, remoteAddr: "5.6.7.8:2525", heloName: "WIN-BOTNET"}
	if err := s.Mail("someone@example.com", &gosmtp.MailOptions{}); err != nil {
		t.Fatalf("weak signals must not reject: %v", err)
	}
	if !s.suspicious {
		t.Fatal("no FCrDNS + implausible HELO should set suspicious")
	}

	// FCrDNS ok → not suspicious even if HELO is sloppy
	s2 := &Session{backend: b, remoteAddr: "1.2.3.9:2525", heloName: "WIN-BOTNET"}
	if err := s2.Mail("someone@example.com", &gosmtp.MailOptions{}); err != nil {
		t.Fatalf("FCrDNS-verified sender must not be rejected: %v", err)
	}
	if s2.suspicious {
		t.Fatal("FCrDNS pass should clear the suspicion")
	}

	// Reset clears the flag
	s.Reset()
	if s.suspicious {
		t.Fatal("Reset must clear suspicious")
	}
	t.Log("✔ weak signals → quarantine flag only; FCrDNS pass clears; Reset clears")
}
