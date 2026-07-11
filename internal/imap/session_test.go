package imap

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
	"github.com/google/uuid"

	"github.com/krisamin/mail/internal/store/postgres"
)

// Integration tests. Requires the dev Postgres:
//   MAIL_TEST_DSN=postgres://mail:maildev@localhost:55432/mail go test ./internal/imap/ -v
// Skipped when the DSN is unset.
//
// A real imapclient connects over TCP and round-trips the full
// LOGIN→LIST→SELECT→APPEND→FETCH→STORE→SEARCH→COPY→EXPUNGE flow.

const (
	testAddr = "maro@krisam.in"
	testPass = "imap-test-app-pw"
)

// setupServer seeds the store and starts an IMAP server on a random port.
func setupServer(t *testing.T) (addr string) {
	t.Helper()
	dsn := os.Getenv("MAIL_TEST_DSN")
	if dsn == "" {
		t.Skip("MAIL_TEST_DSN unset — skipping integration test")
	}
	ctx := context.Background()

	st, err := postgres.New(ctx, dsn)
	if err != nil {
		t.Fatalf("store connect: %v", err)
	}
	t.Cleanup(st.Close)

	// test isolation
	_, _ = st.Pool().Exec(ctx, `TRUNCATE domain, account, app_password, mailbox, message, message_flag, message_blob, outbound_queue, address, relay RESTART IDENTITY CASCADE`)

	// seed: domain + account + address + app password + INBOX (0006 model)
	local := testAddr[:strings.LastIndex(testAddr, "@")]
	domain := testAddr[strings.LastIndex(testAddr, "@")+1:]
	var domainID, accountID uuid.UUID
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO domain (name) VALUES ($1) RETURNING id`, domain).Scan(&domainID); err != nil {
		t.Fatalf("domain seed: %v", err)
	}
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO account (oidc_subject, oidc_email) VALUES ('test:' || $1::text, $1) RETURNING id`,
		testAddr).Scan(&accountID); err != nil {
		t.Fatalf("account seed: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO address (domain_id, local_part, account_id) VALUES ($1, $2, $3)`,
		domainID, local, accountID); err != nil {
		t.Fatalf("address seed: %v", err)
	}
	hash, err := postgres.HashPassword(testPass)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO app_password (account_id, label, hash) VALUES ($1, 'imap-test', $2)`,
		accountID, hash); err != nil {
		t.Fatalf("app password seed: %v", err)
	}
	if _, err := st.CreateMailbox(ctx, accountID, "INBOX"); err != nil {
		t.Fatalf("INBOX create: %v", err)
	}

	// IMAP server — random port
	backend := NewBackend(st)
	server := imapserver.New(&imapserver.Options{
		NewSession:   backend.NewSession,
		InsecureAuth: true,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Close() })

	return ln.Addr().String()
}

func dial(t *testing.T, addr string) *imapclient.Client {
	t.Helper()
	c, err := imapclient.DialInsecure(addr, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

const testRawMessage = "From: Someone <someone@example.com>\r\n" +
	"To: Maro <maro@krisam.in>\r\n" +
	"Subject: IMAP roundtrip\r\n" +
	"Date: Wed, 01 Jul 2026 12:00:00 +0900\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"full roundtrip body — go-imap client to postgres store\r\n"

func TestIMAPFullFlow(t *testing.T) {
	addr := setupServer(t)
	c := dial(t, addr)

	// 1) auth failure → success
	if err := c.Login(testAddr, "wrong-password").Wait(); err == nil {
		t.Fatal("LOGIN passed with a wrong password")
	}
	if err := c.Login(testAddr, testPass).Wait(); err != nil {
		t.Fatalf("LOGIN failed: %v", err)
	}
	t.Log("✔ LOGIN (app password)")

	// 2) LIST — INBOX must be visible
	boxList, err := c.List("", "*", nil).Collect()
	if err != nil || len(boxList) != 1 || boxList[0].Mailbox != "INBOX" {
		t.Fatalf("LIST unexpected: %v %+v", err, boxList)
	}
	t.Log("✔ LIST: INBOX")

	// 3) SELECT
	sel, err := c.Select("inbox", nil).Wait() // lowercase → verifies INBOX normalization
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if sel.NumMessages != 0 {
		t.Fatalf("INBOX should be empty: %d", sel.NumMessages)
	}
	t.Logf("✔ SELECT: uidvalidity=%d uidnext=%d", sel.UIDValidity, sel.UIDNext)

	// 4) APPEND
	ac := c.Append("INBOX", int64(len(testRawMessage)), &goimap.AppendOptions{
		Flags: []goimap.Flag{goimap.FlagSeen},
		Time:  time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
	})
	if _, err := ac.Write([]byte(testRawMessage)); err != nil {
		t.Fatalf("APPEND write: %v", err)
	}
	if err := ac.Close(); err != nil {
		t.Fatalf("APPEND close: %v", err)
	}
	appendData, err := ac.Wait()
	if err != nil {
		t.Fatalf("APPEND: %v", err)
	}
	if appendData.UID != 1 {
		t.Fatalf("first UID should be 1: got %d", appendData.UID)
	}
	t.Logf("✔ APPEND: uid=%d", appendData.UID)

	// 5) FETCH — envelope + flags + full body
	seq := goimap.SeqSetNum(1)
	messageList, err := c.Fetch(seq, &goimap.FetchOptions{
		Envelope: true, Flags: true, RFC822Size: true, UID: true,
		BodySection: []*goimap.FetchItemBodySection{{}},
	}).Collect()
	if err != nil || len(messageList) != 1 {
		t.Fatalf("FETCH: %v (%d messageList)", err, len(messageList))
	}
	m := messageList[0]
	if m.Envelope == nil || m.Envelope.Subject != "IMAP roundtrip" {
		t.Fatalf("envelope unexpected: %+v", m.Envelope)
	}
	if len(m.BodySection) != 1 || string(m.BodySection[0].Bytes) != testRawMessage {
		t.Fatalf("body roundtrip mismatch: %d sections", len(m.BodySection))
	}
	seen := false
	for _, f := range m.Flags {
		if f == goimap.FlagSeen {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("missing \\Seen flag: %v", m.Flags)
	}
	t.Logf("✔ FETCH: subject=%q size=%d flags=%v (body bytes match)", m.Envelope.Subject, m.RFC822Size, m.Flags)

	// 6) STORE — add \Flagged
	stored, err := c.Store(seq, &goimap.StoreFlags{
		Op: goimap.StoreFlagsAdd, Flags: []goimap.Flag{goimap.FlagFlagged},
	}, nil).Collect()
	if err != nil || len(stored) != 1 {
		t.Fatalf("STORE: %v", err)
	}
	flagged := false
	for _, f := range stored[0].Flags {
		if f == goimap.FlagFlagged {
			flagged = true
		}
	}
	if !flagged {
		t.Fatalf("\\Flagged not applied: %v", stored[0].Flags)
	}
	t.Logf("✔ STORE: %v", stored[0].Flags)

	// 7) SEARCH — subject match / mismatch
	found, err := c.UIDSearch(&goimap.SearchCriteria{
		Header: []goimap.SearchCriteriaHeaderField{{Key: "Subject", Value: "roundtrip"}},
	}, nil).Wait()
	if err != nil || len(found.AllUIDs()) != 1 {
		t.Fatalf("SEARCH hit unexpected: %v %v", err, found)
	}
	miss, err := c.UIDSearch(&goimap.SearchCriteria{
		Header: []goimap.SearchCriteriaHeaderField{{Key: "Subject", Value: "no-such-subject"}},
	}, nil).Wait()
	if err != nil || len(miss.AllUIDs()) != 0 {
		t.Fatalf("SEARCH miss unexpected: %v %v", err, miss)
	}
	t.Log("✔ SEARCH: header criteria hit/miss")

	// 8) CREATE + COPY
	if err := c.Create("Archive", nil).Wait(); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := c.Copy(seq, "Archive").Wait(); err != nil {
		t.Fatalf("COPY: %v", err)
	}
	st, err := c.Status("Archive", &goimap.StatusOptions{NumMessages: true}).Wait()
	if err != nil || st.NumMessages == nil || *st.NumMessages != 1 {
		t.Fatalf("Archive should hold 1 message: %v %+v", err, st)
	}
	// ★regression: options beyond MESSAGES/UNSEEN must also be filled —
	// the go-imap encoder nil-dereferences any requested-but-nil item
	// (prod panic from a real client requesting SIZE/DELETED).
	st2, err := c.Status("Archive", &goimap.StatusOptions{
		NumMessages: true, NumUnseen: true, NumDeleted: true, Size: true,
	}).Wait()
	if err != nil || st2.NumDeleted == nil || st2.Size == nil {
		t.Fatalf("STATUS DELETED/SIZE should be filled: %v %+v", err, st2)
	}
	if *st2.Size <= 0 {
		t.Fatalf("STATUS SIZE should be positive: %d", *st2.Size)
	}
	t.Log("✔ CREATE + COPY + STATUS: Archive has 1 message")

	// 9) \Deleted + EXPUNGE
	if _, err := c.Store(seq, &goimap.StoreFlags{
		Op: goimap.StoreFlagsAdd, Flags: []goimap.Flag{goimap.FlagDeleted},
	}, nil).Collect(); err != nil {
		t.Fatalf("STORE \\Deleted: %v", err)
	}
	expunged, err := c.Expunge().Collect()
	if err != nil || len(expunged) != 1 || expunged[0] != 1 {
		t.Fatalf("EXPUNGE unexpected: %v %v", err, expunged)
	}
	sel2, err := c.Select("INBOX", nil).Wait()
	if err != nil || sel2.NumMessages != 0 {
		t.Fatalf("INBOX should be empty after EXPUNGE: %v %d", err, sel2.NumMessages)
	}
	t.Logf("✔ EXPUNGE: seq %v removed, INBOX empty (Archive copy retained)", expunged)

	if err := c.Logout().Wait(); err != nil {
		t.Fatalf("LOGOUT: %v", err)
	}
	t.Log("✔ LOGOUT — full roundtrip passed")
}

// TestIMAPMultiSession verifies the session snapshot model:
// mail appended by session B becomes visible as EXISTS in session A's NOOP (Poll).
func TestIMAPMultiSession(t *testing.T) {
	addr := setupServer(t)

	a := dial(t, addr)
	if err := a.Login(testAddr, testPass).Wait(); err != nil {
		t.Fatalf("A LOGIN: %v", err)
	}
	selA, err := a.Select("INBOX", nil).Wait()
	if err != nil || selA.NumMessages != 0 {
		t.Fatalf("A SELECT: %v", err)
	}

	// session B does APPEND
	b := dial(t, addr)
	if err := b.Login(testAddr, testPass).Wait(); err != nil {
		t.Fatalf("B LOGIN: %v", err)
	}
	ac := b.Append("INBOX", int64(len(testRawMessage)), nil)
	if _, err := ac.Write([]byte(testRawMessage)); err != nil {
		t.Fatalf("B APPEND write: %v", err)
	}
	if err := ac.Close(); err != nil {
		t.Fatalf("B APPEND close: %v", err)
	}
	if _, err := ac.Wait(); err != nil {
		t.Fatalf("B APPEND: %v", err)
	}

	// session A: NOOP → receives EXISTS via Poll → visible through FETCH
	if err := a.Noop().Wait(); err != nil {
		t.Fatalf("A NOOP: %v", err)
	}
	messageList, err := a.Fetch(goimap.SeqSetNum(1), &goimap.FetchOptions{UID: true}).Collect()
	if err != nil || len(messageList) != 1 {
		t.Fatalf("A cannot see B's mail: %v (%d messageList)", err, len(messageList))
	}
	t.Logf("✔ multi-session: B's APPEND visible after A's NOOP as seq=1 uid=%d", messageList[0].UID)
}
