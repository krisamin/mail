package postgres

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// 통합 테스트. dev Postgres 필요:
//   MAIL_TEST_DSN=postgres://mail:maildev@localhost:55432/mail go test ./internal/store/postgres/ -v
// DSN 미설정 시 skip.

func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("MAIL_TEST_DSN")
	if dsn == "" {
		t.Skip("MAIL_TEST_DSN 미설정 — 통합 테스트 skip")
	}
	s, err := New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("연결 실패: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// seedUser는 도메인+유저+앱비밀번호+INBOX를 만든다.
func seedUser(t *testing.T, s *Store, address, password string) int64 {
	t.Helper()
	ctx := context.Background()
	local := address[:strings.LastIndex(address, "@")]
	domain := address[strings.LastIndex(address, "@")+1:]

	var domainID int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO domains (name) VALUES ($1)
		 ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
		 RETURNING id`, domain).Scan(&domainID)
	if err != nil {
		t.Fatalf("도메인 시드: %v", err)
	}

	var userID int64
	err = s.pool.QueryRow(ctx,
		`INSERT INTO users (domain_id, local_part) VALUES ($1, $2)
		 ON CONFLICT (domain_id, local_part) DO UPDATE SET local_part = EXCLUDED.local_part
		 RETURNING id`, domainID, local).Scan(&userID)
	if err != nil {
		t.Fatalf("유저 시드: %v", err)
	}

	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("해시: %v", err)
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO app_passwords (user_id, label, hash) VALUES ($1, 'test', $2)`,
		userID, hash)
	if err != nil {
		t.Fatalf("앱비번 시드: %v", err)
	}

	if _, err := s.CreateMailbox(ctx, userID, "INBOX"); err != nil {
		t.Fatalf("INBOX 생성: %v", err)
	}
	return userID
}

func TestFullFlow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// 매 실행 깨끗하게 (테스트 격리)
	_, _ = s.pool.Exec(ctx, `TRUNCATE domains, users, app_passwords, mailboxes, messages, message_flags, message_blobs RESTART IDENTITY CASCADE`)

	addr := "maro@krisam.in"
	pass := "super-secret-app-pw"
	userID := seedUser(t, s, addr, pass)
	t.Logf("시드 유저 id=%d", userID)

	// 1) 인증 성공/실패
	if _, err := s.AuthenticateAppPassword(ctx, addr, pass); err != nil {
		t.Fatalf("인증 실패(성공해야 함): %v", err)
	}
	if _, err := s.AuthenticateAppPassword(ctx, addr, "wrong"); err == nil {
		t.Fatal("틀린 비번인데 인증 통과됨")
	}
	t.Log("✔ 인증 검증 통과")

	// 2) 메일박스 목록 (INBOX 있어야)
	boxes, err := s.ListMailboxes(ctx, userID)
	if err != nil || len(boxes) != 1 || boxes[0].Name != "INBOX" {
		t.Fatalf("메일박스 목록 이상: %v, %+v", err, boxes)
	}
	inbox := boxes[0]
	t.Logf("✔ INBOX uid_validity=%d uid_next=%d", inbox.UIDValidity, inbox.UIDNext)

	// 3) 메시지 Append
	raw := []byte(strings.Join([]string{
		"From: Someone <someone@example.com>",
		"To: Maro <maro@krisam.in>",
		"Subject: 첫 메일",
		"Date: Wed, 01 Jul 2026 12:00:00 +0900",
		"",
		"본문이야 시로~",
	}, "\r\n"))
	msg, err := s.AppendMessage(ctx, inbox.ID, raw, []string{`\Seen`}, time.Now())
	if err != nil {
		t.Fatalf("Append 실패: %v", err)
	}
	if msg.UID != 1 {
		t.Fatalf("첫 UID는 1이어야 함, got %d", msg.UID)
	}
	if msg.Subject != "첫 메일" {
		t.Fatalf("헤더 캐시 파싱 실패: subject=%q", msg.Subject)
	}
	t.Logf("✔ Append: uid=%d subject=%q from=%q flags=%v", msg.UID, msg.Subject, msg.FromAddr, msg.Flags)

	// 두 번째 메시지 → UID 2
	msg2, err := s.AppendMessage(ctx, inbox.ID, raw, nil, time.Now())
	if err != nil || msg2.UID != 2 {
		t.Fatalf("두 번째 UID는 2여야 함: %v uid=%d", err, msg2.UID)
	}
	t.Logf("✔ UID 단조증가 확인: %d", msg2.UID)

	// 4) 상태 (2건, unseen 1건 — 두번째는 \Seen 없음)
	st, err := s.MailboxStatus(ctx, inbox.ID)
	if err != nil {
		t.Fatalf("상태: %v", err)
	}
	if st.NumMessages != 2 || st.NumUnseen != 1 || st.UIDNext != 3 {
		t.Fatalf("상태 이상: msgs=%d unseen=%d uidnext=%d", st.NumMessages, st.NumUnseen, st.UIDNext)
	}
	t.Logf("✔ 상태: msgs=%d unseen=%d uidnext=%d", st.NumMessages, st.NumUnseen, st.UIDNext)

	// 5) 본문 복원 검증
	blob, err := s.GetMessageBlob(ctx, msg.ID)
	if err != nil || !bytes.Equal(blob, raw) {
		t.Fatalf("본문 불일치: err=%v", err)
	}
	t.Log("✔ 본문 원문 복원 일치")

	// 6) 플래그 교체
	if err := s.SetFlags(ctx, msg2.ID, []string{`\Seen`, `\Flagged`}); err != nil {
		t.Fatalf("SetFlags: %v", err)
	}
	msgs, _ := s.ListMessages(ctx, inbox.ID)
	for _, m := range msgs {
		if m.ID == msg2.ID && len(m.Flags) != 2 {
			t.Fatalf("플래그 교체 실패: %v", m.Flags)
		}
	}
	t.Log("✔ 플래그 교체 확인")

	// 7) Expunge (msg에 \Deleted 달고 지우기)
	_ = s.SetFlags(ctx, msg.ID, []string{`\Deleted`})
	expunged, err := s.ExpungeDeleted(ctx, inbox.ID, nil)
	if err != nil || len(expunged) != 1 || expunged[0] != 1 {
		t.Fatalf("Expunge 이상: %v %v", err, expunged)
	}
	t.Logf("✔ Expunge: uid %v 삭제됨", expunged)

	// 최종 1건 남음
	final, _ := s.MailboxStatus(ctx, inbox.ID)
	if final.NumMessages != 1 {
		t.Fatalf("최종 메시지 수 이상: %d", final.NumMessages)
	}
	t.Log("✔ 전체 흐름 통과")
}
