package postgres

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Integration test. Requires dev Postgres:
//   MAIL_TEST_DSN=postgres://mail:maildev@localhost:55432/mail go test ./internal/store/postgres/ -v
// Skips when DSN is not set.

func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("MAIL_TEST_DSN")
	if dsn == "" {
		t.Skip("MAIL_TEST_DSN not set — skipping integration test")
	}
	s, err := New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connection failed: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// seedAccount creates domain+account+address+app password+INBOX (0006 model).
func seedAccount(t *testing.T, s *Store, address, password string) int64 {
	t.Helper()
	ctx := context.Background()
	local := address[:strings.LastIndex(address, "@")]
	domain := address[strings.LastIndex(address, "@")+1:]

	var domainID int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO domain (name) VALUES ($1)
		 ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
		 RETURNING id`, domain).Scan(&domainID)
	if err != nil {
		t.Fatalf("domain seed: %v", err)
	}

	var accountID int64
	err = s.pool.QueryRow(ctx,
		`INSERT INTO account (oidc_subject, oidc_email) VALUES ('test:' || $1::text, $1)
		 ON CONFLICT (oidc_subject) DO UPDATE SET oidc_email = EXCLUDED.oidc_email
		 RETURNING id`, address).Scan(&accountID)
	if err != nil {
		t.Fatalf("account seed: %v", err)
	}

	if _, err := s.pool.Exec(ctx,
		`INSERT INTO address (domain_id, local_part, account_id) VALUES ($1, $2, $3)
		 ON CONFLICT (domain_id, local_part) DO NOTHING`, domainID, local, accountID); err != nil {
		t.Fatalf("address seed: %v", err)
	}

	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO app_password (account_id, label, hash) VALUES ($1, 'test', $2)`,
		accountID, hash)
	if err != nil {
		t.Fatalf("app password seed: %v", err)
	}

	if _, err := s.CreateMailbox(ctx, accountID, "INBOX"); err != nil {
		t.Fatalf("INBOX creation: %v", err)
	}
	return accountID
}

func TestFullFlow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Start clean each run (test isolation)
	_, _ = s.pool.Exec(ctx, `TRUNCATE domain, account, app_password, mailbox, message, message_flag, message_blob, outbound_queue, address, relay RESTART IDENTITY CASCADE`)

	addr := "maro@krisam.in"
	pass := "super-secret-app-pw"
	accountID := seedAccount(t, s, addr, pass)
	t.Logf("seed user id=%d", accountID)

	// 1) auth success/failure
	if _, err := s.AuthenticateAppPassword(ctx, addr, pass); err != nil {
		t.Fatalf("auth failed (should succeed): %v", err)
	}
	if _, err := s.AuthenticateAppPassword(ctx, addr, "wrong"); err == nil {
		t.Fatal("auth passed with a wrong password")
	}
	t.Log("✔ auth verification passed")

	// 2) mailbox list (INBOX must exist)
	boxList, err := s.ListMailbox(ctx, accountID)
	if err != nil || len(boxList) != 1 || boxList[0].Name != "INBOX" {
		t.Fatalf("mailbox list wrong: %v, %+v", err, boxList)
	}
	inbox := boxList[0]
	t.Logf("✔ INBOX uid_validity=%d uid_next=%d", inbox.UIDValidity, inbox.UIDNext)

	// 3) message Append
	raw := []byte(strings.Join([]string{
		"From: Someone <someone@example.com>",
		"To: Maro <maro@krisam.in>",
		"Subject: first mail",
		"Date: Wed, 01 Jul 2026 12:00:00 +0900",
		"",
		"This is the body, Shiro~",
	}, "\r\n"))
	msg, err := s.AppendMessage(ctx, inbox.ID, raw, []string{`\Seen`}, time.Now())
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}
	if msg.UID != 1 {
		t.Fatalf("first UID must be 1, got %d", msg.UID)
	}
	if msg.Subject != "first mail" {
		t.Fatalf("header cache parsing failed: subject=%q", msg.Subject)
	}
	t.Logf("✔ Append: uid=%d subject=%q from=%q flags=%v", msg.UID, msg.Subject, msg.FromAddr, msg.Flags)

	// Second message → UID 2
	msg2, err := s.AppendMessage(ctx, inbox.ID, raw, nil, time.Now())
	if err != nil || msg2.UID != 2 {
		t.Fatalf("second UID must be 2: %v uid=%d", err, msg2.UID)
	}
	t.Logf("✔ UID monotonic increase confirmed: %d", msg2.UID)

	// 4) status (2 messages, 1 unseen — second has no \Seen)
	st, err := s.MailboxStatus(ctx, inbox.ID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.MessageCount != 2 || st.UnseenCount != 1 || st.UIDNext != 3 {
		t.Fatalf("status wrong: messageList=%d unseen=%d uidnext=%d", st.MessageCount, st.UnseenCount, st.UIDNext)
	}
	t.Logf("✔ status: messageList=%d unseen=%d uidnext=%d", st.MessageCount, st.UnseenCount, st.UIDNext)

	// 5) body restore verification
	blob, err := s.GetMessageBlob(ctx, msg.ID)
	if err != nil || !bytes.Equal(blob, raw) {
		t.Fatalf("body mismatch: err=%v", err)
	}
	t.Log("✔ body restored identical to original")

	// 6) flag replacement
	if err := s.SetFlag(ctx, msg2.ID, []string{`\Seen`, `\Flagged`}); err != nil {
		t.Fatalf("SetFlag: %v", err)
	}
	messageList, _ := s.ListMessage(ctx, inbox.ID)
	for _, m := range messageList {
		if m.ID == msg2.ID && len(m.Flags) != 2 {
			t.Fatalf("flag replacement failed: %v", m.Flags)
		}
	}
	t.Log("✔ flag replacement confirmed")

	// 7) Expunge (mark msg \Deleted and remove)
	_ = s.SetFlag(ctx, msg.ID, []string{`\Deleted`})
	expunged, err := s.ExpungeDeleted(ctx, inbox.ID, nil)
	if err != nil || len(expunged) != 1 || expunged[0] != 1 {
		t.Fatalf("Expunge wrong: %v %v", err, expunged)
	}
	t.Logf("✔ Expunge: uid %v deleted", expunged)

	// 1 message remaining at the end
	final, _ := s.MailboxStatus(ctx, inbox.ID)
	if final.MessageCount != 1 {
		t.Fatalf("final message count wrong: %d", final.MessageCount)
	}
	t.Log("✔ full flow passed")
}
