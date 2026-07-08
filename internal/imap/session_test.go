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

	"github.com/krisamin/mail/internal/store/postgres"
)

// 통합 테스트. dev Postgres 필요:
//   MAIL_TEST_DSN=postgres://mail:maildev@localhost:55432/mail go test ./internal/imap/ -v
// DSN 미설정 시 skip.
//
// 실제 imapclient가 TCP로 붙어 LOGIN→LIST→SELECT→APPEND→FETCH→STORE→
// SEARCH→COPY→EXPUNGE 전체 플로우를 왕복 검증한다.

const (
	testAddr = "maro@krisam.in"
	testPass = "imap-test-app-pw"
)

// setupServer는 store 시드 + 임의 포트에 IMAP 서버를 띄운다.
func setupServer(t *testing.T) (addr string) {
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

	// 테스트 격리
	_, _ = st.Pool().Exec(ctx, `TRUNCATE domain, account, app_password, mailbox, message, message_flag, message_blob, outbound_queue, alias, relay RESTART IDENTITY CASCADE`)

	// 시드: 도메인 + 유저 + 앱비밀번호 + INBOX
	local := testAddr[:strings.LastIndex(testAddr, "@")]
	domain := testAddr[strings.LastIndex(testAddr, "@")+1:]
	var domainID, accountID int64
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO domain (name) VALUES ($1) RETURNING id`, domain).Scan(&domainID); err != nil {
		t.Fatalf("도메인 시드: %v", err)
	}
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO account (domain_id, local_part) VALUES ($1, $2) RETURNING id`,
		domainID, local).Scan(&accountID); err != nil {
		t.Fatalf("유저 시드: %v", err)
	}
	hash, err := postgres.HashPassword(testPass)
	if err != nil {
		t.Fatalf("해시: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO app_password (account_id, label, hash) VALUES ($1, 'imap-test', $2)`,
		accountID, hash); err != nil {
		t.Fatalf("앱비번 시드: %v", err)
	}
	if _, err := st.CreateMailbox(ctx, accountID, "INBOX"); err != nil {
		t.Fatalf("INBOX 생성: %v", err)
	}

	// IMAP 서버 — 임의 포트
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

	// 1) 인증 실패 → 성공
	if err := c.Login(testAddr, "wrong-password").Wait(); err == nil {
		t.Fatal("틀린 비번인데 LOGIN 통과")
	}
	if err := c.Login(testAddr, testPass).Wait(); err != nil {
		t.Fatalf("LOGIN 실패: %v", err)
	}
	t.Log("✔ LOGIN (앱 비밀번호)")

	// 2) LIST — INBOX 보여야
	boxList, err := c.List("", "*", nil).Collect()
	if err != nil || len(boxList) != 1 || boxList[0].Mailbox != "INBOX" {
		t.Fatalf("LIST 이상: %v %+v", err, boxList)
	}
	t.Log("✔ LIST: INBOX")

	// 3) SELECT
	sel, err := c.Select("inbox", nil).Wait() // 소문자 → INBOX 정규화 검증
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if sel.NumMessages != 0 {
		t.Fatalf("빈 INBOX여야 함: %d", sel.NumMessages)
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
		t.Fatalf("첫 UID는 1: got %d", appendData.UID)
	}
	t.Logf("✔ APPEND: uid=%d", appendData.UID)

	// 5) FETCH — envelope + flags + 본문 전문
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
		t.Fatalf("envelope 이상: %+v", m.Envelope)
	}
	if len(m.BodySection) != 1 || string(m.BodySection[0].Bytes) != testRawMessage {
		t.Fatalf("본문 왕복 불일치: %d sections", len(m.BodySection))
	}
	seen := false
	for _, f := range m.Flags {
		if f == goimap.FlagSeen {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("\\Seen 플래그 없음: %v", m.Flags)
	}
	t.Logf("✔ FETCH: subject=%q size=%d flags=%v (본문 바이트 일치)", m.Envelope.Subject, m.RFC822Size, m.Flags)

	// 6) STORE — \Flagged 추가
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
		t.Fatalf("\\Flagged 반영 안 됨: %v", stored[0].Flags)
	}
	t.Logf("✔ STORE: %v", stored[0].Flags)

	// 7) SEARCH — subject 매칭 / 미스매칭
	found, err := c.UIDSearch(&goimap.SearchCriteria{
		Header: []goimap.SearchCriteriaHeaderField{{Key: "Subject", Value: "roundtrip"}},
	}, nil).Wait()
	if err != nil || len(found.AllUIDs()) != 1 {
		t.Fatalf("SEARCH 히트 이상: %v %v", err, found)
	}
	miss, err := c.UIDSearch(&goimap.SearchCriteria{
		Header: []goimap.SearchCriteriaHeaderField{{Key: "Subject", Value: "no-such-subject"}},
	}, nil).Wait()
	if err != nil || len(miss.AllUIDs()) != 0 {
		t.Fatalf("SEARCH 미스 이상: %v %v", err, miss)
	}
	t.Log("✔ SEARCH: 헤더 조건 히트/미스")

	// 8) CREATE + COPY
	if err := c.Create("Archive", nil).Wait(); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := c.Copy(seq, "Archive").Wait(); err != nil {
		t.Fatalf("COPY: %v", err)
	}
	st, err := c.Status("Archive", &goimap.StatusOptions{NumMessages: true}).Wait()
	if err != nil || st.NumMessages == nil || *st.NumMessages != 1 {
		t.Fatalf("Archive에 1건 있어야: %v %+v", err, st)
	}
	t.Log("✔ CREATE + COPY + STATUS: Archive 1건")

	// 9) \Deleted + EXPUNGE
	if _, err := c.Store(seq, &goimap.StoreFlags{
		Op: goimap.StoreFlagsAdd, Flags: []goimap.Flag{goimap.FlagDeleted},
	}, nil).Collect(); err != nil {
		t.Fatalf("STORE \\Deleted: %v", err)
	}
	expunged, err := c.Expunge().Collect()
	if err != nil || len(expunged) != 1 || expunged[0] != 1 {
		t.Fatalf("EXPUNGE 이상: %v %v", err, expunged)
	}
	sel2, err := c.Select("INBOX", nil).Wait()
	if err != nil || sel2.NumMessages != 0 {
		t.Fatalf("EXPUNGE 후 INBOX 비어야: %v %d", err, sel2.NumMessages)
	}
	t.Logf("✔ EXPUNGE: seq %v 제거, INBOX 0건 (Archive 사본은 유지)", expunged)

	if err := c.Logout().Wait(); err != nil {
		t.Fatalf("LOGOUT: %v", err)
	}
	t.Log("✔ LOGOUT — 전체 왕복 통과")
}

// TestIMAPMultiSession은 세션 스냅샷 모델 검증:
// 세션 B가 넣은 메일이 세션 A의 NOOP(Poll)에서 EXISTS로 보이는지.
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

	// 세션 B가 APPEND
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

	// 세션 A: NOOP → Poll 경유로 EXISTS 수신 → FETCH로 보이는지
	if err := a.Noop().Wait(); err != nil {
		t.Fatalf("A NOOP: %v", err)
	}
	messageList, err := a.Fetch(goimap.SeqSetNum(1), &goimap.FetchOptions{UID: true}).Collect()
	if err != nil || len(messageList) != 1 {
		t.Fatalf("A가 B의 메일을 못 봄: %v (%d messageList)", err, len(messageList))
	}
	t.Logf("✔ 멀티세션: B의 APPEND가 A의 NOOP 이후 seq=1 uid=%d로 보임", messageList[0].UID)
}
