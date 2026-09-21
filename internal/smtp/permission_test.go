package smtp

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"
)

// Permission tests (0005 — group model). They drive the real servers the way
// a mail client does, because the switches are only worth anything at the
// wire.
//
// The default group is seeded by the migration: mail inside the server and
// mail from outside are fine, going out to the world needs a group that says
// so. setupServers truncates accounts, which cascades to group membership,
// so every test starts with plain accounts standing on the floor.

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

// grant sets one three-state column on a group.
func grant(t *testing.T, env *testEnv, groupName, column, value string) {
	t.Helper()
	if _, err := env.store.Pool().Exec(context.Background(),
		`UPDATE account_group SET `+column+` = $2 WHERE name = $1`, groupName, value); err != nil {
		t.Fatalf("grant %s.%s: %v", groupName, column, err)
	}
}

// makeGroup creates a group at the given position (higher sits above).
func makeGroup(t *testing.T, env *testEnv, name string, position int) {
	t.Helper()
	if _, err := env.store.Pool().Exec(context.Background(),
		`INSERT INTO account_group (name, position) VALUES ($1, $2)
		 ON CONFLICT (name) DO UPDATE SET position = EXCLUDED.position`, name, position); err != nil {
		t.Fatalf("create group %s: %v", name, err)
	}
}

// join puts the account owning addr into a group.
func join(t *testing.T, env *testEnv, groupName, addr string) {
	t.Helper()
	if _, err := env.store.Pool().Exec(context.Background(),
		`INSERT INTO account_group_member (group_id, account_id)
		 SELECT g.id, a.id FROM account_group g, account a
		 WHERE g.name = $1 AND a.oidc_email = $2
		 ON CONFLICT DO NOTHING`, groupName, addr); err != nil {
		t.Fatalf("join %s: %v", groupName, err)
	}
}

func authenticate(t *testing.T, c *gosmtp.Client, addr string) {
	t.Helper()
	if err := c.Auth(sasl.NewPlainClient("", addr, testPass)); err != nil {
		t.Fatalf("AUTH: %v", err)
	}
}

// The floor: colleagues yes, the outside world no.
func TestDefaultGroupBlocksExternalSend(t *testing.T) {
	_, subAddr := setupRelaying(t)

	c := dialSubmission(t, subAddr)
	authenticate(t, c, testAddr)
	if err := c.Mail(testAddr, nil); err != nil {
		t.Fatalf("MAIL: %v", err)
	}
	if err := c.Rcpt(testAddr2, nil); err != nil {
		t.Fatalf("internal recipient must be accepted by default: %v", err)
	}
	err := c.Rcpt("someone@example.com", nil)
	if err == nil {
		t.Fatal("external recipient accepted although the default group forbids it")
	}
	if !strings.Contains(err.Error(), "inside this server") {
		t.Fatalf("unexpected refusal: %v", err)
	}
	t.Logf("✔ default group keeps mail inside: %v", err)
}

// Joining a group that allows external sending opens the border for that
// account only.
func TestGroupGrantsExternalSend(t *testing.T) {
	env, subAddr := setupRelaying(t)
	join(t, env, "external-sender", testAddr)

	c := dialSubmission(t, subAddr)
	authenticate(t, c, testAddr)
	if err := c.Mail(testAddr, nil); err != nil {
		t.Fatalf("MAIL: %v", err)
	}
	if err := c.Rcpt("someone@example.com", nil); err != nil {
		t.Fatalf("group member must reach the outside: %v", err)
	}
	t.Log("✔ group membership opened external sending")

	// the account that did not join is still held inside
	c2 := dialSubmission(t, subAddr)
	authenticate(t, c2, testAddr2)
	if err := c2.Mail(testAddr2, nil); err != nil {
		t.Fatalf("MAIL: %v", err)
	}
	if err := c2.Rcpt("someone@example.com", nil); err == nil {
		t.Fatal("a non-member reached the outside")
	}
}

// Two groups disagree: the one sitting higher decides.
func TestHigherGroupWins(t *testing.T) {
	env, subAddr := setupRelaying(t)
	join(t, env, "external-sender", testAddr) // position 10, allows

	makeGroup(t, env, "quarantine", 50)
	grant(t, env, "quarantine", "can_send_external", "deny")
	join(t, env, "quarantine", testAddr)

	c := dialSubmission(t, subAddr)
	authenticate(t, c, testAddr)
	if err := c.Mail(testAddr, nil); err != nil {
		t.Fatalf("MAIL: %v", err)
	}
	err := c.Rcpt("someone@example.com", nil)
	if err == nil {
		t.Fatal("the higher group said deny and was ignored")
	}
	t.Logf("✔ higher group overruled the lower one: %v", err)
}

// Turning sending off on the floor stops even internal mail.
func TestDefaultGroupCanStopSending(t *testing.T) {
	env, subAddr := setupSubmission(t)
	grant(t, env, "everyone", "can_send", "deny")

	c := dialSubmission(t, subAddr)
	authenticate(t, c, testAddr)
	if err := c.Mail(testAddr, nil); err != nil {
		t.Fatalf("MAIL: %v", err)
	}
	if err := c.Rcpt(testAddr2, nil); err == nil {
		t.Fatal("recipient accepted although sending is disabled")
	}
	t.Log("✔ sending disabled refuses every recipient")
	grant(t, env, "everyone", "can_send", "allow")
}

// Mail from outside is refused for a member of a group that says no, while
// everyone else keeps receiving.
func TestGroupBlocksExternalReceive(t *testing.T) {
	env := setupServers(t)
	makeGroup(t, env, "no-inbound", 20)
	grant(t, env, "no-inbound", "can_receive_external", "deny")
	join(t, env, "no-inbound", testAddr)

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
		t.Fatal("inbound mail accepted although the group refuses it")
	} else {
		t.Logf("✔ inbound refused: %v", err)
	}
	if err := c.Rcpt(testAddr2, nil); err != nil {
		t.Fatalf("unrelated recipient must still receive: %v", err)
	}
}

// The daily limit comes from the group stack and refuses with a temporary
// error, so a well-behaved sender retries tomorrow instead of bouncing.
func TestGroupDailySendLimit(t *testing.T) {
	env, subAddr := setupSubmission(t)
	makeGroup(t, env, "limited", 30)
	if _, err := env.store.Pool().Exec(context.Background(),
		`UPDATE account_group SET daily_send_limit = 1 WHERE name = 'limited'`); err != nil {
		t.Fatalf("limit: %v", err)
	}
	join(t, env, "limited", testAddr)

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
		t.Fatal("second send passed although the group limit is 1")
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
