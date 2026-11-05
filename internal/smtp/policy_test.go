package smtp

import (
	"context"
	"errors"
	"testing"

	gosmtp "github.com/emersion/go-smtp"

	"github.com/krisamin/mail/internal/spam"
)

// TestPolicySourceGreylist: the backend takes greylisting from the policy
// source per session, so an admin change applies without a restart.
func TestPolicySourceGreylist(t *testing.T) {
	env := setupServers(t)
	_, _ = env.store.Pool().Exec(context.Background(), `TRUNCATE greylist`)

	saved := `{"greylist":false,"greylistDelaySecond":60}`
	src := spam.NewSource(spam.Policy{Greylist: true, GreylistDelaySecond: 60}, nil,
		func(ctx context.Context) (string, bool, error) { return saved, saved != "", nil })
	b := NewBackend(env.store, "mx.test").WithPolicySource(src)

	rcpt := func() error {
		s := &Session{backend: b, remoteAddr: "203.0.113.9:33333", heloName: "x"}
		if err := s.Mail("stranger@example.org", &gosmtp.MailOptions{}); err != nil {
			t.Fatalf("mail: %v", err)
		}
		return s.Rcpt(testAddr, &gosmtp.RcptOptions{})
	}

	// saved policy: greylist off, first contact passes
	if err := rcpt(); err != nil {
		t.Fatalf("greylist off should pass: %v", err)
	}

	// admin resets to env (greylist on): the next session gets 451
	saved = ""
	src.Invalidate()
	_, _ = env.store.Pool().Exec(context.Background(), `TRUNCATE greylist`)
	err := rcpt()
	var serr *gosmtp.SMTPError
	if !errors.As(err, &serr) || serr.Code != 451 {
		t.Fatalf("greylist on should give 451: %v", err)
	}
}
