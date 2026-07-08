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

// 발송 큐 테스트. dev Postgres 필요:
//   MAIL_TEST_DSN=postgres://mail:maildev@localhost:55432/mail go test ./internal/queue/ -v

func testStore(t *testing.T) *postgres.Store {
	t.Helper()
	dsn := os.Getenv("MAIL_TEST_DSN")
	if dsn == "" {
		t.Skip("MAIL_TEST_DSN 미설정 — 통합 테스트 skip")
	}
	st, err := postgres.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("store 연결: %v", err)
	}
	t.Cleanup(st.Close)
	_, _ = st.Pool().Exec(context.Background(),
		`TRUNCATE domain, account, app_password, mailbox, message, message_flag, message_blob, outbound_queue, alias, relay RESTART IDENTITY CASCADE`)
	return st
}

// mockSender는 호출 기록 + 지정된 에러를 돌려주는 Sender.
type mockSender struct {
	mu    sync.Mutex
	calls []string // "from→rcpt"
	errs  []error  // 호출 순서대로 소비. 소진되면 nil(성공)
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
		t.Fatalf("큐 상태 조회: %v", err)
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
		t.Fatalf("2건 처리해야: %v n=%d", err, n)
	}
	if sender.callCount() != 2 {
		t.Fatalf("sender 2회 호출돼야: %d", sender.callCount())
	}
	for _, id := range []int64{1, 2} {
		status, attemptCount, _ := queueStatus(t, st, id)
		if status != store.OutboundSent || attemptCount != 0 {
			t.Fatalf("id=%d sent여야: %s attemptCount=%d", id, status, attemptCount)
		}
	}
	// 재실행 시 due 없음
	n, _ = w.ProcessOnce(ctx)
	if n != 0 {
		t.Fatalf("sent 후 due가 남으면 안 됨: %d", n)
	}
	t.Log("✔ 성공 발송: 2건 sent, 재소비 없음")
}

// TestQueueRetryBackoff: 일시 오류 → 재시도 예약(백오프) → 성공.
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
		t.Fatal("1건 처리해야")
	}
	status, attemptCount, lastErr := queueStatus(t, st, 1)
	if status != store.OutboundPending || attemptCount != 1 || !strings.Contains(lastErr, "connection refused") {
		t.Fatalf("재시도 대기여야: %s attemptCount=%d err=%q", status, attemptCount, lastErr)
	}

	// next_attempt_at이 미래 → 당장은 due 아님
	if n, _ := w.ProcessOnce(ctx); n != 0 {
		t.Fatal("백오프 중인데 due로 잡힘")
	}

	// 시각을 과거로 돌려 due로 만들고 재처리 → 성공
	if _, err := st.Pool().Exec(ctx,
		`UPDATE outbound_queue SET next_attempt_at = now() - interval '1 second' WHERE id = 1`); err != nil {
		t.Fatalf("시각 조작: %v", err)
	}
	if n, _ := w.ProcessOnce(ctx); n != 1 {
		t.Fatal("due 복귀 후 1건 처리해야")
	}
	status, _, _ = queueStatus(t, st, 1)
	if status != store.OutboundSent {
		t.Fatalf("재시도 후 sent여야: %s", status)
	}
	t.Log("✔ 일시 오류 → 백오프 재시도 → 성공")
}

// TestQueuePermanentError: 5xx급 영구 오류는 즉시 failed.
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
		t.Fatal("1건 처리해야")
	}
	status, _, lastErr := queueStatus(t, st, 1)
	if status != store.OutboundFailed || !strings.Contains(lastErr, "550") {
		t.Fatalf("즉시 failed여야: %s err=%q", status, lastErr)
	}
	t.Log("✔ 영구 오류 즉시 failed (재시도 없음)")
}

// TestQueueMaxAttempts: 재시도 소진 시 failed.
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

	// 1차: 일시 오류 → 재시도 대기
	_, _ = w.ProcessOnce(ctx)
	// due로 되돌리고 2차: MaxAttemptCount 도달 → failed
	_, _ = st.Pool().Exec(ctx, `UPDATE outbound_queue SET next_attempt_at = now() WHERE id = 1`)
	_, _ = w.ProcessOnce(ctx)

	status, attemptCount, _ := queueStatus(t, st, 1)
	if status != store.OutboundFailed || attemptCount != 2 {
		t.Fatalf("소진 후 failed여야: %s attemptCount=%d", status, attemptCount)
	}
	t.Log("✔ MaxAttemptCount 소진 → failed")
}

// TestSubmissionToQueueToRelay: 진짜 end-to-end —
// 인증 유저가 외부 도메인으로 제출 → 큐 적재 → 워커가 RelaySender로 발송.
// relay는 "다른 메일 서버" 역할의 로컬 MX 서버 (external.test 도메인을 로컬로 시드).
func TestSubmissionToQueueToRelay(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	// 시드: 우리 도메인 krisam.in의 maro + relay 쪽 도메인 external.test의 friend
	seedAccount(t, st, "maro@krisam.in", "queue-test-pw")
	seedAccount(t, st, "friend@external.test", "friend-pw")
	// external.test를 다시 비활성화해 "외부 도메인"으로 만든다... 대신
	// 더 단순하게: relay 서버는 별도 store 없이 같은 store의 MX 백엔드를 쓰되,
	// submission 쪽에서 krisam.in만 로컬로 인식하도록 external.test를 비활성 처리.
	if _, err := st.Pool().Exec(ctx,
		`UPDATE domain SET active = false WHERE name = 'external.test'`); err != nil {
		t.Fatalf("도메인 비활성: %v", err)
	}

	// relay 역할: external.test 메일을 받는 MX 서버 (검증은 우회하고 수신만 기록)
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

	// submission 서버 (외부 도메인 큐 적재 활성)
	subSrv := gosmtp.NewServer(mailsmtp.NewSubmissionBackend(st, "submit.krisam.in", true))
	subSrv.Domain = "submit.krisam.in"
	subSrv.AllowInsecureAuth = true
	subLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("submission listen: %v", err)
	}
	go func() { _ = subSrv.Serve(subLn) }()
	t.Cleanup(func() { _ = subSrv.Close() })

	// 1) 인증 제출: maro → friend@external.test
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
		t.Fatalf("RCPT(외부): %v", err)
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

	// 2) 큐에 적재됐는지
	var count int
	_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM outbound_queue WHERE status = 'pending'`).Scan(&count)
	if count != 1 {
		t.Fatalf("큐에 1건 있어야: %d", count)
	}
	t.Log("✔ 외부 도메인 제출 → 큐 적재")

	// 3) 워커가 RelaySender(진짜 SMTP 클라이언트)로 relay에 발송
	sender := NewRelaySender(RelayConfig{Addr: relayLn.Addr().String(), StartTLS: false})
	w := NewWorker(st, sender, Config{})
	if n, err := w.ProcessOnce(ctx); err != nil || n != 1 {
		t.Fatalf("워커 1건 처리해야: %v n=%d", err, n)
	}

	select {
	case got := <-received:
		if !strings.Contains(got, "Subject: queued") {
			t.Fatalf("relay가 받은 본문 이상:\n%.200s", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("relay가 메일을 못 받음")
	}
	status, _, _ := queueStatus(t, st, 1)
	if status != store.OutboundSent {
		t.Fatalf("sent여야: %s", status)
	}
	t.Log("✔ 워커 → relay 실 SMTP 발송 → sent (제출→큐→발송 전체 왕복)")
}

// ── 테스트 헬퍼 ─────────────────────────────────────────────

// recordingBackend는 받은 DATA를 기록만 하는 SMTP 백엔드 (relay 역할).
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
		t.Fatalf("도메인 시드: %v", err)
	}
	var accountID int64
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO account (domain_id, local_part) VALUES ($1, $2)
		 ON CONFLICT (domain_id, local_part) DO UPDATE SET local_part = EXCLUDED.local_part
		 RETURNING id`, domainID, local).Scan(&accountID); err != nil {
		t.Fatalf("유저 시드: %v", err)
	}
	hash, err := postgres.HashPassword(password)
	if err != nil {
		t.Fatalf("해시: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO app_password (account_id, label, hash) VALUES ($1, 'queue-test', $2)`,
		accountID, hash); err != nil {
		t.Fatalf("앱비번 시드: %v", err)
	}
}
