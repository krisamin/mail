package smtp

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"
)

// Extra addresses + multi-domain internal routing e2e.
//
// Maro's scenario: when the server hosts both krisam.in and kirby.so,
// mail between them must be delivered internally without going through
// relay (Resend). Also verifies extra addresses (hello@) and the
// catch-all (*@kirby.so).

// TestAliasDelivery: delivery via extra addresses/wildcards on the MX inbound path.
func TestAliasDelivery(t *testing.T) {
	env := setupServers(t)
	ctx := context.Background()

	// seed: kirby.so domain + 2 extra addresses (on the maro account)
	var kirbyID, krisamID uuid.UUID
	if err := env.store.Pool().QueryRow(ctx,
		`INSERT INTO domain (name) VALUES ('kirby.so') RETURNING id`).Scan(&kirbyID); err != nil {
		t.Fatalf("kirby.so seed: %v", err)
	}
	if err := env.store.Pool().QueryRow(ctx,
		`SELECT id FROM domain WHERE name = 'krisam.in'`).Scan(&krisamID); err != nil {
		t.Fatalf("krisam.in lookup: %v", err)
	}
	maro, err := env.store.FindAccountByAddress(ctx, testAddr)
	if err != nil {
		t.Fatalf("maro lookup: %v", err)
	}
	if _, err := env.store.CreateAddress(ctx, krisamID, "hello", maro.ID); err != nil {
		t.Fatalf("extra address: %v", err)
	}
	if _, err := env.store.CreateAddress(ctx, kirbyID, "*", maro.ID); err != nil {
		t.Fatalf("catch-all: %v", err)
	}

	// 1) receive to the exact alias hello@krisam.in → maro INBOX
	if err := sendSMTP(t, env.smtpAddr, "ext@example.com", []string{"hello@krisam.in"},
		"From: ext@example.com\r\nTo: hello@krisam.in\r\nSubject: to alias\r\n\r\nalias mail\r\n"); err != nil {
		t.Fatalf("alias reception: %v", err)
	}

	// 2) catch-all: any address @kirby.so → maro INBOX
	if err := sendSMTP(t, env.smtpAddr, "ext@example.com", []string{"whatever-12345@kirby.so"},
		"From: ext@example.com\r\nTo: whatever-12345@kirby.so\r\nSubject: to catchall\r\n\r\ncatchall mail\r\n"); err != nil {
		t.Fatalf("catch-all reception: %v", err)
	}

	// 3) an address with no alias is still 550
	if err := trySend(env.smtpAddr, "ext@example.com", "nobody@krisam.in"); err == nil {
		t.Fatal("nobody@krisam.in was accepted (should be 550)")
	} else if !strings.Contains(err.Error(), "550") {
		t.Fatalf("should be 550: %v", err)
	}

	// check both mails in maro's INBOX
	messageList := readInbox(t, env.imapAddr, testAddr, testPass)
	var subjects []string
	for _, m := range messageList {
		if m.Envelope != nil {
			subjects = append(subjects, m.Envelope.Subject)
		}
	}
	for _, want := range []string{"to alias", "to catchall"} {
		found := false
		for _, s := range subjects {
			if strings.Contains(s, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("INBOX missing %q: %v", want, subjects)
		}
	}
	t.Logf("✔ alias + catch-all delivery verified (INBOX %d mails): %v", len(subjects), subjects)
	t.Log("✔ unregistered address still 550")
}

// TestInternalRoutingTwoDomains: submission between two domains on our
// server is delivered internally without going through the outbound queue (relay).
func TestInternalRoutingTwoDomains(t *testing.T) {
	env, subAddr := setupSubmission(t) // enqueueEnabled=false — configuration without relay
	ctx := context.Background()

	// kirby.so + catch-all (maro)
	var kirbyID uuid.UUID
	if err := env.store.Pool().QueryRow(ctx,
		`INSERT INTO domain (name) VALUES ('kirby.so') RETURNING id`).Scan(&kirbyID); err != nil {
		t.Fatalf("kirby.so seed: %v", err)
	}
	maro, _ := env.store.FindAccountByAddress(ctx, testAddr)
	if _, err := env.store.CreateAddress(ctx, kirbyID, "*", maro.ID); err != nil {
		t.Fatalf("catch-all: %v", err)
	}

	// shiro authenticates and submits to team@kirby.so —
	// kirby.so is our domain, so it must be delivered even with the queue disabled (internal routing)
	c := dialSubmission(t, subAddr)
	if err := c.Auth(sasl.NewPlainClient("", testAddr2, testPass)); err != nil {
		t.Fatalf("AUTH: %v", err)
	}
	if err := c.Mail(testAddr2, nil); err != nil {
		t.Fatalf("MAIL: %v", err)
	}
	if err := c.Rcpt("team@kirby.so", nil); err != nil {
		t.Fatalf("RCPT team@kirby.so (internal domain but rejected): %v", err)
	}
	w, err := c.Data()
	if err != nil {
		t.Fatalf("DATA: %v", err)
	}
	msg := "From: shiro@krisam.in\r\nTo: team@kirby.so\r\nSubject: cross-domain internal\r\n\r\ninternal routing!\r\n"
	if _, err := w.Write([]byte(msg)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// the outbound queue must be empty (relay not involved)
	var queued int
	_ = env.store.Pool().QueryRow(ctx, `SELECT count(*) FROM outbound_queue`).Scan(&queued)
	if queued != 0 {
		t.Fatalf("internal routing but %d entries went into the queue", queued)
	}

	// verify arrival in maro's INBOX (the catch-all is maro)
	messageList := readInbox(t, env.imapAddr, testAddr, testPass)
	found := false
	for _, m := range messageList {
		if m.Envelope != nil && strings.Contains(m.Envelope.Subject, "cross-domain internal") {
			found = true
		}
	}
	if !found {
		t.Fatalf("not delivered internally (%d mails)", len(messageList))
	}
	t.Log("✔ krisam.in → kirby.so submission delivered internally without relay (queue empty)")
}

// TestSubmissionSendAsAlias: can send with an owned extra address as the
// envelope from; someone else's address yields 553.
func TestSubmissionSendAsAlias(t *testing.T) {
	env, subAddr := setupSubmission(t)
	ctx := context.Background()

	var krisamID uuid.UUID
	if err := env.store.Pool().QueryRow(ctx,
		`SELECT id FROM domain WHERE name = 'krisam.in'`).Scan(&krisamID); err != nil {
		t.Fatalf("krisam.in lookup: %v", err)
	}
	maro, _ := env.store.FindAccountByAddress(ctx, testAddr)
	if _, err := env.store.CreateAddress(ctx, krisamID, "hello", maro.ID); err != nil {
		t.Fatalf("extra address: %v", err)
	}

	// maro sends as hello@krisam.in → allowed
	c := dialSubmission(t, subAddr)
	if err := c.Auth(sasl.NewPlainClient("", testAddr, testPass)); err != nil {
		t.Fatalf("AUTH: %v", err)
	}
	if err := c.Mail("hello@krisam.in", nil); err != nil {
		t.Fatalf("sending as own alias was rejected: %v", err)
	}
	t.Log("✔ MAIL FROM allowed with own alias")

	// shiro sends as hello@krisam.in → 553
	c2 := dialSubmission(t, subAddr)
	if err := c2.Auth(sasl.NewPlainClient("", testAddr2, testPass)); err != nil {
		t.Fatalf("AUTH2: %v", err)
	}
	err := c2.Mail("hello@krisam.in", nil)
	if err == nil {
		t.Fatal("sending as someone else's alias was allowed")
	}
	if !strings.Contains(err.Error(), "553") {
		t.Fatalf("expected 553: %v", err)
	}
	t.Logf("✔ sending as someone else's alias rejected with 553: %v", err)
}

// trySend attempts to send one mail and returns the RCPT-stage error.
func trySend(smtpAddr, from, to string) error {
	c, err := gosmtp.Dial(smtpAddr)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Hello("test.example.com"); err != nil {
		return err
	}
	if err := c.Mail(from, nil); err != nil {
		return err
	}
	return c.Rcpt(to, nil)
}
