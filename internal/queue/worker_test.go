package queue

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"

	mailsmtp "github.com/krisamin/mail/internal/smtp"
	"github.com/krisamin/mail/internal/store"
	"github.com/krisamin/mail/internal/store/postgres"
)

// Outbound queue tests. Requires dev Postgres:
//   MAIL_TEST_DSN=postgres://mail:maildev@localhost:55432/mail go test ./internal/queue/ -v

func testStore(t *testing.T) *postgres.Store {
	t.Helper()
	dsn := os.Getenv("MAIL_TEST_DSN")
	if dsn == "" {
		t.Skip("MAIL_TEST_DSN not set — skipping integration tests")
	}
	st, err := postgres.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("store connect: %v", err)
	}
	t.Cleanup(st.Close)
	_, _ = st.Pool().Exec(context.Background(),
		`TRUNCATE domain, account, app_password, mailbox, message, message_flag, message_blob, outbound_queue, address, relay RESTART IDENTITY CASCADE`)
	return st
}

// mockSender is a Sender that records calls + returns the specified errors.
type mockSender struct {
	mu    sync.Mutex
	calls []string // "from→rcpt"
	errs  []error  // consumed in call order. nil (success) once exhausted
}

func (m *mockSender) Send(_ context.Context, from, rcpt string, _ []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, from+"→"+rcpt)
	if len(m.errs) > 0 {
		err := m.errs[0]
		m.errs = m.errs[1:]
		return err
	}
	return nil
}

func (m *mockSender) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func queueStatus(t *testing.T, st *postgres.Store, id int64) (status string, attemptCount int, lastError string) {
	t.Helper()
	err := st.Pool().QueryRow(context.Background(),
		`SELECT status, attempt_count, COALESCE(last_error, '') FROM outbound_queue WHERE id = $1`, id).
		Scan(&status, &attemptCount, &lastError)
	if err != nil {
		t.Fatalf("queue status lookup: %v", err)
	}
	return
}

const rawMsg = "From: maro@krisam.in\r\nTo: friend@example.com\r\nSubject: queued\r\n\r\nhello\r\n"

// TestQueueSuccess: enqueue → ProcessOnce → sent.
func TestQueueSuccess(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	if err := st.EnqueueOutbound(ctx, "maro@krisam.in",
		[]string{"a@example.com", "b@example.com"}, []byte(rawMsg)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	sender := &mockSender{}
	w := NewWorker(st, sender, Config{})
	n, err := w.ProcessOnce(ctx)
	if err != nil || n != 2 {
		t.Fatalf("should process 2 entries: %v n=%d", err, n)
	}
	if sender.callCount() != 2 {
		t.Fatalf("sender should be called twice: %d", sender.callCount())
	}
	for _, id := range []int64{1, 2} {
		status, attemptCount, _ := queueStatus(t, st, id)
		if status != store.OutboundSent || attemptCount != 0 {
			t.Fatalf("id=%d should be sent: %s attemptCount=%d", id, status, attemptCount)
		}
	}
	// no due entries on rerun
	n, _ = w.ProcessOnce(ctx)
	if n != 0 {
		t.Fatalf("no due entries should remain after sent: %d", n)
	}
	t.Log("✔ successful send: 2 entries sent, no re-consumption")
}

// TestQueueRetryBackoff: transient error → retry scheduled (backoff) → success.
func TestQueueRetryBackoff(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	if err := st.EnqueueOutbound(ctx, "maro@krisam.in",
		[]string{"c@example.com"}, []byte(rawMsg)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	sender := &mockSender{errs: []error{errors.New("connection refused")}}
	w := NewWorker(st, sender, Config{BaseBackoff: time.Minute})

	if n, _ := w.ProcessOnce(ctx); n != 1 {
		t.Fatal("should process 1 entry")
	}
	status, attemptCount, lastErr := queueStatus(t, st, 1)
	if status != store.OutboundPending || attemptCount != 1 || !strings.Contains(lastErr, "connection refused") {
		t.Fatalf("should be awaiting retry: %s attemptCount=%d err=%q", status, attemptCount, lastErr)
	}

	// next_attempt_at is in the future → not due right now
	if n, _ := w.ProcessOnce(ctx); n != 0 {
		t.Fatal("picked up as due while in backoff")
	}

	// rewind the time to the past to make it due, reprocess → success
	if _, err := st.Pool().Exec(ctx,
		`UPDATE outbound_queue SET next_attempt_at = now() - interval '1 second' WHERE id = 1`); err != nil {
		t.Fatalf("time manipulation: %v", err)
	}
	if n, _ := w.ProcessOnce(ctx); n != 1 {
		t.Fatal("should process 1 entry after becoming due again")
	}
	status, _, _ = queueStatus(t, st, 1)
	if status != store.OutboundSent {
		t.Fatalf("should be sent after retry: %s", status)
	}
	t.Log("✔ transient error → backoff retry → success")
}

// TestQueuePermanentError: a 5xx-class permanent error is failed immediately.
func TestQueuePermanentError(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	if err := st.EnqueueOutbound(ctx, "maro@krisam.in",
		[]string{"d@example.com"}, []byte(rawMsg)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	sender := &mockSender{errs: []error{&PermanentError{Err: errors.New("550 user unknown")}}}
	w := NewWorker(st, sender, Config{})
	if n, _ := w.ProcessOnce(ctx); n != 1 {
		t.Fatal("should process 1 entry")
	}
	status, _, lastErr := queueStatus(t, st, 1)
	if status != store.OutboundFailed || !strings.Contains(lastErr, "550") {
		t.Fatalf("should be failed immediately: %s err=%q", status, lastErr)
	}
	t.Log("✔ permanent error failed immediately (no retry)")
}

// TestQueueMaxAttempts: failed once retries are exhausted.
func TestQueueMaxAttempts(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	if err := st.EnqueueOutbound(ctx, "maro@krisam.in",
		[]string{"e@example.com"}, []byte(rawMsg)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	sender := &mockSender{errs: []error{
		errors.New("timeout 1"), errors.New("timeout 2"),
	}}
	w := NewWorker(st, sender, Config{MaxAttemptCount: 2, BaseBackoff: time.Minute})

	// 1st: transient error → awaiting retry
	_, _ = w.ProcessOnce(ctx)
	// make it due again, 2nd: MaxAttemptCount reached → failed
	_, _ = st.Pool().Exec(ctx, `UPDATE outbound_queue SET next_attempt_at = now() WHERE id = 1`)
	_, _ = w.ProcessOnce(ctx)

	status, attemptCount, _ := queueStatus(t, st, 1)
	if status != store.OutboundFailed || attemptCount != 2 {
		t.Fatalf("should be failed after exhaustion: %s attemptCount=%d", status, attemptCount)
	}
	t.Log("✔ MaxAttemptCount exhausted → failed")
}

// TestSubmissionToQueueToRelay: real end-to-end —
// an authenticated user submits to an external domain → enqueued → the worker
// sends via RelaySender. The relay is a local MX server playing the "other
// mail server" role (external.test domain seeded locally).
func TestSubmissionToQueueToRelay(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	// seed: maro on our domain krisam.in + friend on the relay-side domain external.test
	seedAccount(t, st, "maro@krisam.in", "queue-test-pw")
	seedAccount(t, st, "friend@external.test", "friend-pw")
	// deactivate external.test again to make it an "external domain"... or
	// rather, more simply: the relay server uses the same store's MX backend
	// without a separate store, while external.test is deactivated so the
	// submission side recognizes only krisam.in as local.
	if _, err := st.Pool().Exec(ctx,
		`UPDATE domain SET active = false WHERE name = 'external.test'`); err != nil {
		t.Fatalf("domain deactivate: %v", err)
	}

	// relay role: an MX server receiving external.test mail (bypasses validation, only records reception)
	received := make(chan string, 1)
	relayBackend := &recordingBackend{received: received}
	relaySrv := gosmtp.NewServer(relayBackend)
	relaySrv.Domain = "relay.test"
	relayLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("relay listen: %v", err)
	}
	go func() { _ = relaySrv.Serve(relayLn) }()
	t.Cleanup(func() { _ = relaySrv.Close() })

	// submission server (external-domain enqueueing enabled)
	subSrv := gosmtp.NewServer(mailsmtp.NewSubmissionBackend(st, "submit.krisam.in", true))
	subSrv.Domain = "submit.krisam.in"
	subSrv.AllowInsecureAuth = true
	subLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("submission listen: %v", err)
	}
	go func() { _ = subSrv.Serve(subLn) }()
	t.Cleanup(func() { _ = subSrv.Close() })

	// 1) authenticated submission: maro → friend@external.test
	c, err := gosmtp.Dial(subLn.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if err := c.Hello("client.test"); err != nil {
		t.Fatalf("HELO: %v", err)
	}
	if err := c.Auth(sasl.NewPlainClient("", "maro@krisam.in", "queue-test-pw")); err != nil {
		t.Fatalf("AUTH: %v", err)
	}
	if err := c.Mail("maro@krisam.in", nil); err != nil {
		t.Fatalf("MAIL: %v", err)
	}
	if err := c.Rcpt("friend@external.test", nil); err != nil {
		t.Fatalf("RCPT (external): %v", err)
	}
	wtr, err := c.Data()
	if err != nil {
		t.Fatalf("DATA: %v", err)
	}
	if _, err := wtr.Write([]byte(rawMsg)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := wtr.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_ = c.Quit()

	// 2) verify it was enqueued
	var count int
	_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM outbound_queue WHERE status = 'pending'`).Scan(&count)
	if count != 1 {
		t.Fatalf("queue should have 1 entry: %d", count)
	}
	t.Log("✔ external domain submission → enqueued")

	// 3) the worker sends to the relay via RelaySender (a real SMTP client)
	sender := NewRelaySender(RelayConfig{Addr: relayLn.Addr().String(), StartTLS: false})
	w := NewWorker(st, sender, Config{})
	if n, err := w.ProcessOnce(ctx); err != nil || n != 1 {
		t.Fatalf("worker should process 1 entry: %v n=%d", err, n)
	}

	select {
	case got := <-received:
		if !strings.Contains(got, "Subject: queued") {
			t.Fatalf("relay received unexpected body:\n%.200s", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("relay did not receive the mail")
	}
	status, _, _ := queueStatus(t, st, 1)
	if status != store.OutboundSent {
		t.Fatalf("should be sent: %s", status)
	}
	t.Log("✔ worker → real SMTP send to relay → sent (full submission→queue→send round trip)")
}

// ── test helpers ─────────────────────────────────────────────

// recordingBackend is an SMTP backend that only records received DATA (relay role).
type recordingBackend struct {
	received chan string
}

func (b *recordingBackend) NewSession(_ *gosmtp.Conn) (gosmtp.Session, error) {
	return &recordingSession{received: b.received}, nil
}

type recordingSession struct {
	received chan string
}

func (s *recordingSession) Mail(string, *gosmtp.MailOptions) error { return nil }
func (s *recordingSession) Rcpt(string, *gosmtp.RcptOptions) error { return nil }
func (s *recordingSession) Data(r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.received <- string(b)
	return nil
}
func (s *recordingSession) Reset()        {}
func (s *recordingSession) Logout() error { return nil }

func seedAccount(t *testing.T, st *postgres.Store, address, password string) {
	t.Helper()
	ctx := context.Background()
	local := address[:strings.LastIndex(address, "@")]
	domain := address[strings.LastIndex(address, "@")+1:]

	var domainID int64
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO domain (name) VALUES ($1)
		 ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name RETURNING id`,
		domain).Scan(&domainID); err != nil {
		t.Fatalf("domain seed: %v", err)
	}
	var accountID int64
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO account (oidc_subject, oidc_email) VALUES ('test:' || $1::text, $1)
		 ON CONFLICT (oidc_subject) DO UPDATE SET oidc_email = EXCLUDED.oidc_email
		 RETURNING id`, address).Scan(&accountID); err != nil {
		t.Fatalf("account seed: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO address (domain_id, local_part, account_id) VALUES ($1, $2, $3)
		 ON CONFLICT (domain_id, local_part) DO NOTHING`, domainID, local, accountID); err != nil {
		t.Fatalf("address seed: %v", err)
	}
	hash, err := postgres.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO app_password (account_id, label, hash) VALUES ($1, 'queue-test', $2)`,
		accountID, hash); err != nil {
		t.Fatalf("app password seed: %v", err)
	}
}
