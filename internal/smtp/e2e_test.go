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

// end-to-end 통합 테스트. dev Postgres 필요:
//   MAIL_TEST_DSN=postgres://mail:maildev@localhost:55432/mail go test ./internal/smtp/ -v
//
// 진짜 메일 서버 흐름 전체를 왕복한다:
//   외부 MTA(net/smtp 클라이언트) → SMTP 수신 → store 배달
//   → IMAP 클라이언트(imapclient)로 같은 메일을 읽기

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
		t.Skip("MAIL_TEST_DSN 미설정 — 통합 테스트 skip")
	}
	ctx := context.Background()

	st, err := postgres.New(ctx, dsn)
	if err != nil {
		t.Fatalf("store 연결: %v", err)
	}
	t.Cleanup(st.Close)

	_, _ = st.Pool().Exec(ctx, `TRUNCATE domains, users, app_passwords, mailboxes, messages, message_flags, message_blobs, outbound_queue, aliases RESTART IDENTITY CASCADE`)

	// 시드: krisam.in 도메인 + 유저 2명 (maro는 INBOX 있음, shiro는 INBOX 없음 — 자동생성 검증)
	var domainID int64
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO domains (name) VALUES ('krisam.in') RETURNING id`).Scan(&domainID); err != nil {
		t.Fatalf("도메인 시드: %v", err)
	}
	hash, err := postgres.HashPassword(testPass)
	if err != nil {
		t.Fatalf("해시: %v", err)
	}
	for _, addr := range []string{testAddr, testAddr2} {
		local := addr[:strings.LastIndex(addr, "@")]
		var userID int64
		if err := st.Pool().QueryRow(ctx,
			`INSERT INTO users (domain_id, local_part) VALUES ($1, $2) RETURNING id`,
			domainID, local).Scan(&userID); err != nil {
			t.Fatalf("유저 시드 %s: %v", addr, err)
		}
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO app_passwords (user_id, label, hash) VALUES ($1, 'e2e', $2)`,
			userID, hash); err != nil {
			t.Fatalf("앱비번 시드: %v", err)
		}
		if addr == testAddr {
			if _, err := st.CreateMailbox(ctx, userID, "INBOX"); err != nil {
				t.Fatalf("INBOX 생성: %v", err)
			}
		}
	}

	// SMTP 수신 서버 — 임의 포트
	smtpSrv := gosmtp.NewServer(NewBackend(st, "mx-test.krisam.in"))
	smtpSrv.Domain = "mx-test.krisam.in"
	smtpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("smtp listen: %v", err)
	}
	go func() { _ = smtpSrv.Serve(smtpLn) }()
	t.Cleanup(func() { _ = smtpSrv.Close() })

	// IMAP 서버 — 임의 포트
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

// sendSMTP는 외부 MTA 역할 — go-smtp 클라이언트로 메일을 던진다.
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
	msgs, err := c.Fetch(seq, &goimap.FetchOptions{
		Envelope: true, Flags: true, UID: true,
		BodySection: []*goimap.FetchItemBodySection{{Peek: true}},
	}).Collect()
	if err != nil {
		t.Fatalf("imap fetch: %v", err)
	}
	_ = c.Logout().Wait()
	return msgs
}

const e2eMessage = "From: Someone <someone@example.com>\r\n" +
	"To: Maro <maro@krisam.in>\r\n" +
	"Subject: e2e delivery\r\n" +
	"Date: Wed, 01 Jul 2026 12:00:00 +0900\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"SMTP in, IMAP out — the server is alive.\r\n"

// TestEndToEndDelivery: SMTP로 받은 메일이 IMAP으로 보인다.
func TestEndToEndDelivery(t *testing.T) {
	env := setupServers(t)

	if err := sendSMTP(t, env.smtpAddr, "someone@example.com", []string{testAddr}, e2eMessage); err != nil {
		t.Fatalf("SMTP 발송: %v", err)
	}
	t.Log("✔ SMTP 수신 완료")

	msgs := readInbox(t, env.imapAddr, testAddr, testPass)
	if len(msgs) != 1 {
		t.Fatalf("INBOX에 1건 있어야: %d", len(msgs))
	}
	m := msgs[0]
	if m.Envelope == nil || m.Envelope.Subject != "e2e delivery" {
		t.Fatalf("subject 이상: %+v", m.Envelope)
	}
	// 새 메일은 unseen이어야 함
	for _, f := range m.Flags {
		if f == goimap.FlagSeen {
			t.Fatalf("배달 직후 \\Seen이면 안 됨: %v", m.Flags)
		}
	}
	// Received 헤더가 prepend 됐는지 + 원문 보존 확인
	if len(m.BodySection) != 1 {
		t.Fatalf("본문 섹션 없음")
	}
	full := string(m.BodySection[0].Bytes)
	if !strings.HasPrefix(full, "Received: from sender.example.com") {
		t.Fatalf("Received 헤더 없음:\n%.200s", full)
	}
	if !strings.HasSuffix(full, "the server is alive.\r\n") {
		t.Fatalf("원문 본문 훼손:\n%.200s", full)
	}
	t.Logf("✔ IMAP으로 읽힘: subject=%q flags=%v (Received 헤더 + 원문 보존)", m.Envelope.Subject, m.Flags)
}

// TestRcptValidation: 없는 유저는 RCPT 단계에서 550으로 거절 (backscatter 방지).
func TestRcptValidation(t *testing.T) {
	env := setupServers(t)

	err := sendSMTP(t, env.smtpAddr, "someone@example.com",
		[]string{"nobody@krisam.in"}, e2eMessage)
	if err == nil {
		t.Fatal("없는 유저인데 수락됨")
	}
	smtpErr, ok := err.(*gosmtp.SMTPError)
	if !ok || smtpErr.Code != 550 {
		t.Fatalf("550이어야 함: %v", err)
	}
	t.Logf("✔ 없는 유저 550 거절: %v", err)

	// 다른 도메인도 거절 (오픈 릴레이 아님)
	err = sendSMTP(t, env.smtpAddr, "someone@example.com",
		[]string{"victim@gmail.com"}, e2eMessage)
	if err == nil {
		t.Fatal("외부 도메인 릴레이가 수락됨 — 오픈 릴레이!")
	}
	t.Logf("✔ 외부 도메인 릴레이 거절: %v", err)
}

// TestMultiRecipientAndInboxAutoCreate: 다중 수신자 + INBOX 자동 생성.
func TestMultiRecipientAndInboxAutoCreate(t *testing.T) {
	env := setupServers(t)

	// shiro는 INBOX가 아직 없음 — 배달 시 자동 생성돼야
	if err := sendSMTP(t, env.smtpAddr, "someone@example.com",
		[]string{testAddr, testAddr2}, e2eMessage); err != nil {
		t.Fatalf("SMTP 발송: %v", err)
	}

	for _, addr := range []string{testAddr, testAddr2} {
		msgs := readInbox(t, env.imapAddr, addr, testPass)
		if len(msgs) != 1 {
			t.Fatalf("%s INBOX에 1건 있어야: %d", addr, len(msgs))
		}
		// 수신자별 Received 헤더 확인
		full := string(msgs[0].BodySection[0].Bytes)
		if !strings.Contains(full, "for <"+addr+">") {
			t.Fatalf("%s용 Received 헤더 아님:\n%.200s", addr, full)
		}
	}
	t.Log("✔ 다중 수신자 각자 INBOX 배달 (shiro INBOX 자동 생성 + 수신자별 Received)")
}

// TestIdleReceivesNewMail: IDLE 중인 IMAP 세션이 SMTP 배달을 감지한다.
// (폴링 주기 의존 — idleInterval을 테스트에서 짧게 못 바꾸므로 Poll 경유 NOOP으로 검증)
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
		t.Fatalf("빈 INBOX여야: %v %d", err, sel.NumMessages)
	}

	// 선택된 세션이 살아있는 동안 SMTP로 배달
	if err := sendSMTP(t, env.smtpAddr, "someone@example.com", []string{testAddr}, e2eMessage); err != nil {
		t.Fatalf("SMTP 발송: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// NOOP → Poll → EXISTS 반영 → FETCH로 보임
	if err := c.Noop().Wait(); err != nil {
		t.Fatalf("noop: %v", err)
	}
	msgs, err := c.Fetch(goimap.SeqSetNum(1), &goimap.FetchOptions{UID: true}).Collect()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("NOOP 후 새 메일이 보여야: %v (%d)", err, len(msgs))
	}
	t.Logf("✔ 선택 중인 IMAP 세션이 NOOP 후 SMTP 배달 메일 확인 (uid=%d)", msgs[0].UID)
}
