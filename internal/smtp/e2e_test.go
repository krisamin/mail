package smtp

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	gosmtp "github.com/emersion/go-smtp"

	imapbackend "github.com/krisamin/mail/internal/imap"
	"github.com/krisamin/mail/internal/store/postgres"
)

// End-to-end integration tests. Requires dev Postgres:
//   MAIL_TEST_DSN=postgres://mail:maildev@localhost:55432/mail go test ./internal/smtp/ -v
//
// Round-trips the entire real mail server flow:
//   external MTA (net/smtp client) → SMTP inbound → store delivery
//   → read the same mail back with an IMAP client (imapclient)

const (
	testAddr  = "maro@krisam.in"
	testAddr2 = "shiro@krisam.in"
	testPass  = "e2e-test-app-pw"
)

type testEnv struct {
	smtpAddr string
	imapAddr string
	store    *postgres.Store
}

func setupServers(t *testing.T) *testEnv {
	t.Helper()
	dsn := os.Getenv("MAIL_TEST_DSN")
	if dsn == "" {
		t.Skip("MAIL_TEST_DSN not set — skipping integration tests")
	}
	ctx := context.Background()

	st, err := postgres.New(ctx, dsn)
	if err != nil {
		t.Fatalf("store connect: %v", err)
	}
	t.Cleanup(st.Close)

	_, _ = st.Pool().Exec(ctx, `TRUNCATE domain, account, app_password, mailbox, message, message_flag, message_blob, outbound_queue, address, relay RESTART IDENTITY CASCADE`)

	// seed: krisam.in domain + 2 accounts (maro has an INBOX, shiro doesn't — verifies auto-creation)
	var domainID int64
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO domain (name) VALUES ('krisam.in') RETURNING id`).Scan(&domainID); err != nil {
		t.Fatalf("domain seed: %v", err)
	}
	hash, err := postgres.HashPassword(testPass)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	for _, addr := range []string{testAddr, testAddr2} {
		local := addr[:strings.LastIndex(addr, "@")]
		var accountID int64
		if err := st.Pool().QueryRow(ctx,
			`INSERT INTO account (oidc_subject, oidc_email) VALUES ('test:' || $1::text, $1) RETURNING id`,
			addr).Scan(&accountID); err != nil {
			t.Fatalf("account seed %s: %v", addr, err)
		}
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO address (domain_id, local_part, account_id) VALUES ($1, $2, $3)`,
			domainID, local, accountID); err != nil {
			t.Fatalf("address seed %s: %v", addr, err)
		}
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO app_password (account_id, label, hash) VALUES ($1, 'e2e', $2)`,
			accountID, hash); err != nil {
			t.Fatalf("app password seed: %v", err)
		}
		if addr == testAddr {
			if _, err := st.CreateMailbox(ctx, accountID, "INBOX"); err != nil {
				t.Fatalf("create INBOX: %v", err)
			}
		}
	}

	// inbound SMTP server — arbitrary port
	smtpSrv := gosmtp.NewServer(NewBackend(st, "mx-test.krisam.in"))
	smtpSrv.Domain = "mx-test.krisam.in"
	smtpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("smtp listen: %v", err)
	}
	go func() { _ = smtpSrv.Serve(smtpLn) }()
	t.Cleanup(func() { _ = smtpSrv.Close() })

	// IMAP server — arbitrary port
	imapSrv := imapserver.New(&imapserver.Options{
		NewSession:   imapbackend.NewBackend(st).NewSession,
		InsecureAuth: true,
	})
	imapLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("imap listen: %v", err)
	}
	go func() { _ = imapSrv.Serve(imapLn) }()
	t.Cleanup(func() { _ = imapSrv.Close() })

	return &testEnv{
		smtpAddr: smtpLn.Addr().String(),
		imapAddr: imapLn.Addr().String(),
		store:    st,
	}
}

// sendSMTP plays the external MTA role — throws mail via the go-smtp client.
func sendSMTP(t *testing.T, addr, from string, to []string, msg string) error {
	t.Helper()
	c, err := gosmtp.Dial(addr)
	if err != nil {
		t.Fatalf("smtp dial: %v", err)
	}
	defer c.Close()
	if err := c.Hello("sender.example.com"); err != nil {
		t.Fatalf("HELO: %v", err)
	}
	if err := c.Mail(from, nil); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt, nil); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func readInbox(t *testing.T, imapAddr, user, pass string) []*imapclient.FetchMessageBuffer {
	t.Helper()
	c, err := imapclient.DialInsecure(imapAddr, nil)
	if err != nil {
		t.Fatalf("imap dial: %v", err)
	}
	defer c.Close()
	if err := c.Login(user, pass).Wait(); err != nil {
		t.Fatalf("imap login: %v", err)
	}
	sel, err := c.Select("INBOX", nil).Wait()
	if err != nil {
		t.Fatalf("imap select: %v", err)
	}
	if sel.NumMessages == 0 {
		return nil
	}
	seq := goimap.SeqSet{}
	seq.AddRange(1, sel.NumMessages)
	messageList, err := c.Fetch(seq, &goimap.FetchOptions{
		Envelope: true, Flags: true, UID: true,
		BodySection: []*goimap.FetchItemBodySection{{Peek: true}},
	}).Collect()
	if err != nil {
		t.Fatalf("imap fetch: %v", err)
	}
	_ = c.Logout().Wait()
	return messageList
}

const e2eMessage = "From: Someone <someone@example.com>\r\n" +
	"To: Maro <maro@krisam.in>\r\n" +
	"Subject: e2e delivery\r\n" +
	"Date: Wed, 01 Jul 2026 12:00:00 +0900\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"SMTP in, IMAP out — the server is alive.\r\n"

// TestEndToEndDelivery: mail received via SMTP is visible via IMAP.
func TestEndToEndDelivery(t *testing.T) {
	env := setupServers(t)

	if err := sendSMTP(t, env.smtpAddr, "someone@example.com", []string{testAddr}, e2eMessage); err != nil {
		t.Fatalf("SMTP send: %v", err)
	}
	t.Log("✔ SMTP reception complete")

	messageList := readInbox(t, env.imapAddr, testAddr, testPass)
	if len(messageList) != 1 {
		t.Fatalf("INBOX should have 1 message: %d", len(messageList))
	}
	m := messageList[0]
	if m.Envelope == nil || m.Envelope.Subject != "e2e delivery" {
		t.Fatalf("unexpected subject: %+v", m.Envelope)
	}
	// new mail must be unseen
	for _, f := range m.Flags {
		if f == goimap.FlagSeen {
			t.Fatalf("must not be \\Seen right after delivery: %v", m.Flags)
		}
	}
	// verify the Received header was prepended + the original is preserved
	if len(m.BodySection) != 1 {
		t.Fatalf("missing body section")
	}
	full := string(m.BodySection[0].Bytes)
	if !strings.HasPrefix(full, "Received: from sender.example.com") {
		t.Fatalf("missing Received header:\n%.200s", full)
	}
	if !strings.HasSuffix(full, "the server is alive.\r\n") {
		t.Fatalf("original body corrupted:\n%.200s", full)
	}
	t.Logf("✔ readable via IMAP: subject=%q flags=%v (Received header + original preserved)", m.Envelope.Subject, m.Flags)
}

// TestRcptValidation: unknown users are rejected at the RCPT stage with 550 (backscatter prevention).
func TestRcptValidation(t *testing.T) {
	env := setupServers(t)

	err := sendSMTP(t, env.smtpAddr, "someone@example.com",
		[]string{"nobody@krisam.in"}, e2eMessage)
	if err == nil {
		t.Fatal("unknown user was accepted")
	}
	smtpErr, ok := err.(*gosmtp.SMTPError)
	if !ok || smtpErr.Code != 550 {
		t.Fatalf("should be 550: %v", err)
	}
	t.Logf("✔ unknown user rejected with 550: %v", err)

	// other domains are rejected too (not an open relay)
	err = sendSMTP(t, env.smtpAddr, "someone@example.com",
		[]string{"victim@gmail.com"}, e2eMessage)
	if err == nil {
		t.Fatal("external domain relay was accepted — open relay!")
	}
	t.Logf("✔ external domain relay rejected: %v", err)
}

// TestMultiRecipientAndInboxAutoCreate: multiple recipients + INBOX auto-creation.
func TestMultiRecipientAndInboxAutoCreate(t *testing.T) {
	env := setupServers(t)

	// shiro has no INBOX yet — it must be auto-created on delivery
	if err := sendSMTP(t, env.smtpAddr, "someone@example.com",
		[]string{testAddr, testAddr2}, e2eMessage); err != nil {
		t.Fatalf("SMTP send: %v", err)
	}

	for _, addr := range []string{testAddr, testAddr2} {
		messageList := readInbox(t, env.imapAddr, addr, testPass)
		if len(messageList) != 1 {
			t.Fatalf("%s INBOX should have 1 message: %d", addr, len(messageList))
		}
		// check the per-recipient Received header
		full := string(messageList[0].BodySection[0].Bytes)
		if !strings.Contains(full, "for <"+addr+">") {
			t.Fatalf("Received header not for %s:\n%.200s", addr, full)
		}
	}
	t.Log("✔ each of multiple recipients delivered to own INBOX (shiro INBOX auto-created + per-recipient Received)")
}

// TestIdleReceivesNewMail: an IMAP session in IDLE detects an SMTP delivery.
// (Depends on the polling interval — idleInterval can't be shortened in tests,
// so verified via NOOP through Poll.)
func TestNoopSeesNewMail(t *testing.T) {
	env := setupServers(t)

	c, err := imapclient.DialInsecure(env.imapAddr, nil)
	if err != nil {
		t.Fatalf("imap dial: %v", err)
	}
	defer c.Close()
	if err := c.Login(testAddr, testPass).Wait(); err != nil {
		t.Fatalf("login: %v", err)
	}
	sel, err := c.Select("INBOX", nil).Wait()
	if err != nil || sel.NumMessages != 0 {
		t.Fatalf("INBOX should be empty: %v %d", err, sel.NumMessages)
	}

	// deliver via SMTP while the selected session is alive
	if err := sendSMTP(t, env.smtpAddr, "someone@example.com", []string{testAddr}, e2eMessage); err != nil {
		t.Fatalf("SMTP send: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// NOOP → Poll → EXISTS reflected → visible via FETCH
	if err := c.Noop().Wait(); err != nil {
		t.Fatalf("noop: %v", err)
	}
	messageList, err := c.Fetch(goimap.SeqSetNum(1), &goimap.FetchOptions{UID: true}).Collect()
	if err != nil || len(messageList) != 1 {
		t.Fatalf("new mail should be visible after NOOP: %v (%d)", err, len(messageList))
	}
	t.Logf("✔ selected IMAP session sees SMTP-delivered mail after NOOP (uid=%d)", messageList[0].UID)
}
