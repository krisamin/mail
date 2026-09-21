package smtp

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"
)

// Permission tests (0004). They drive the real servers the same way a mail
// client does, because the switches are only worth anything at the wire.

// setupRelaying is setupSubmission with the outbound queue enabled — without
// it every external recipient is refused for an unrelated reason and the
// permission check never gets a turn.
func setupRelaying(t *testing.T) (*testEnv, string) {
	t.Helper()
	env := setupServers(t)

	srv := gosmtp.NewServer(NewSubmissionBackend(env.store, "submit-test.krisam.in", true))
	srv.Domain = "submit-test.krisam.in"
	srv.AllowInsecureAuth = true
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("submission listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return env, ln.Addr().String()
}

// setPermission flips one column on the account owning addr.
func setPermission(t *testing.T, env *testEnv, addr, column string, value any) {
	t.Helper()
	_, err := env.store.Pool().Exec(context.Background(),
		`UPDATE account SET `+column+` = $2 WHERE oidc_email = $1`, addr, value)
	if err != nil {
		t.Fatalf("set %s: %v", column, err)
	}
}

func authenticate(t *testing.T, c *gosmtp.Client, addr string) {
	t.Helper()
	if err := c.Auth(sasl.NewPlainClient("", addr, testPass)); err != nil {
		t.Fatalf("AUTH: %v", err)
	}
}

// An account with "send outside" off may still write to a colleague, but is
// refused the moment the recipient lives elsewhere.
func TestExternalSendBlocked(t *testing.T) {
	env, subAddr := setupRelaying(t)
	setPermission(t, env, testAddr, "can_send_external", false)

	c := dialSubmission(t, subAddr)
	authenticate(t, c, testAddr)
	if err := c.Mail(testAddr, nil); err != nil {
		t.Fatalf("MAIL: %v", err)
	}
	if err := c.Rcpt(testAddr2, nil); err != nil {
		t.Fatalf("internal recipient must still be accepted: %v", err)
	}
	err := c.Rcpt("someone@example.com", nil)
	if err == nil {
		t.Fatal("external recipient accepted although sending outside is off")
	}
	if !strings.Contains(err.Error(), "inside this server") {
		t.Fatalf("unexpected refusal: %v", err)
	}
	t.Logf("✔ external recipient refused: %v", err)
}

// With the account switch on but the domain switch off, the same send is
// refused — the domain is the operator's kill switch.
func TestDomainSendSwitch(t *testing.T) {
	env, subAddr := setupRelaying(t)
	if _, err := env.store.Pool().Exec(context.Background(),
		`UPDATE domain SET allow_send_external = false WHERE name = 'krisam.in'`); err != nil {
		t.Fatalf("domain switch: %v", err)
	}

	c := dialSubmission(t, subAddr)
	authenticate(t, c, testAddr)
	if err := c.Mail(testAddr, nil); err != nil {
		t.Fatalf("MAIL: %v", err)
	}
	err := c.Rcpt("someone@example.com", nil)
	if err == nil {
		t.Fatal("external recipient accepted although the domain forbids it")
	}
	if !strings.Contains(err.Error(), "domain") {
		t.Fatalf("unexpected refusal: %v", err)
	}
	t.Logf("✔ domain switch refused the send: %v", err)
}

// Sending turned off entirely refuses even internal recipients.
func TestSendDisabled(t *testing.T) {
	env, subAddr := setupSubmission(t)
	setPermission(t, env, testAddr, "can_send", false)

	c := dialSubmission(t, subAddr)
	authenticate(t, c, testAddr)
	if err := c.Mail(testAddr, nil); err != nil {
		t.Fatalf("MAIL: %v", err)
	}
	if err := c.Rcpt(testAddr2, nil); err == nil {
		t.Fatal("recipient accepted although sending is disabled")
	}
	t.Log("✔ sending disabled refuses every recipient")
}

// Mail arriving from outside is refused for a recipient who does not accept
// external mail; the inbox is reachable from inside all the same.
func TestExternalReceiveBlocked(t *testing.T) {
	env := setupServers(t)
	setPermission(t, env, testAddr, "can_receive_external", false)

	c, err := gosmtp.Dial(env.smtpAddr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if err := c.Hello("outside.example.com"); err != nil {
		t.Fatalf("HELO: %v", err)
	}
	if err := c.Mail("stranger@example.com", nil); err != nil {
		t.Fatalf("MAIL: %v", err)
	}
	if err := c.Rcpt(testAddr, nil); err == nil {
		t.Fatal("inbound mail accepted although external receiving is off")
	} else {
		t.Logf("✔ inbound refused: %v", err)
	}
	// the other account still receives normally
	if err := c.Rcpt(testAddr2, nil); err != nil {
		t.Fatalf("unrelated recipient must still receive: %v", err)
	}
}

// The daily limit counts recipients and refuses with a temporary error, so a
// well-behaved sender retries tomorrow instead of bouncing.
func TestDailySendLimit(t *testing.T) {
	env, subAddr := setupSubmission(t)
	setPermission(t, env, testAddr, "daily_send_limit", 1)

	send := func() error {
		c := dialSubmission(t, subAddr)
		authenticate(t, c, testAddr)
		if err := c.Mail(testAddr, nil); err != nil {
			return err
		}
		if err := c.Rcpt(testAddr2, nil); err != nil {
			return err
		}
		w, err := c.Data()
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte(submitMessage)); err != nil {
			return err
		}
		return w.Close()
	}

	if err := send(); err != nil {
		t.Fatalf("first send must pass: %v", err)
	}
	err := send()
	if err == nil {
		t.Fatal("second send passed although the daily limit is 1")
	}
	if !strings.Contains(err.Error(), "daily") {
		t.Fatalf("unexpected refusal: %v", err)
	}
	t.Logf("✔ daily limit refused the second send: %v", err)
}

// An app password scoped to reading cannot be used to send.
func TestAppPasswordScope(t *testing.T) {
	env, subAddr := setupSubmission(t)
	if _, err := env.store.Pool().Exec(context.Background(),
		`UPDATE app_password SET scope_list = ARRAY['imap']
		 WHERE account_id = (SELECT id FROM account WHERE oidc_email = $1)`, testAddr); err != nil {
		t.Fatalf("scope update: %v", err)
	}

	c := dialSubmission(t, subAddr)
	err := c.Auth(sasl.NewPlainClient("", testAddr, testPass))
	if err == nil {
		t.Fatal("a read-only app password was allowed to send")
	}
	if !strings.Contains(err.Error(), "not allowed to send") {
		t.Fatalf("unexpected refusal: %v", err)
	}
	t.Logf("✔ read-only password refused at submission: %v", err)
}
