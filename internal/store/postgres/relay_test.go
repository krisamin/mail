package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/krisamin/mail/internal/store"
)

// TestRelay는 relay CRUD + ResolveRelay 우선순위를 검증한다.
// 필요 환경: MAIL_TEST_DSN (store_test.go와 동일)
func TestRelay(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	_, _ = s.pool.Exec(ctx, `TRUNCATE domain, account, app_password, mailbox, message, message_flag, message_blob, outbound_queue, address, relay RESTART IDENTITY CASCADE`)

	// 시드: 도메인 2개
	var krisamID, kirbyID int64
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO domain (name) VALUES ('krisam.in') RETURNING id`).Scan(&krisamID); err != nil {
		t.Fatalf("도메인 시드: %v", err)
	}
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO domain (name) VALUES ('kirby.so') RETURNING id`).Scan(&kirbyID); err != nil {
		t.Fatalf("도메인 시드: %v", err)
	}

	// 1) relay 없음 → ErrNotFound
	if _, err := s.ResolveRelay(ctx, "krisam.in"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("relay 없을 때 ErrNotFound 기대, got %v", err)
	}
	t.Log("✔ relay 없음 → ErrNotFound")

	// 2) default relay 생성 → 아무 도메인이나 default로 해석
	resend, err := s.CreateRelay(ctx, &store.Relay{
		Name: "resend", Host: "smtp.resend.com", Port: 587,
		Username: "resend", Password: "re_secret", StartTLS: true,
		IsDefault: true, Active: true,
	})
	if err != nil {
		t.Fatalf("relay 생성: %v", err)
	}
	got, err := s.ResolveRelay(ctx, "krisam.in")
	if err != nil || got.ID != resend.ID {
		t.Fatalf("default 해석 실패: %v %+v", err, got)
	}
	t.Log("✔ default relay 해석")

	// 3) 도메인 지정 relay가 default보다 우선
	ses, err := s.CreateRelay(ctx, &store.Relay{
		Name: "ses", Host: "email-smtp.ap-northeast-2.amazonaws.com", Port: 587,
		Username: "AKIA...", Password: "sespw", StartTLS: true, Active: true,
	})
	if err != nil {
		t.Fatalf("relay 생성: %v", err)
	}
	if err := s.SetDomainRelay(ctx, kirbyID, &ses.ID); err != nil {
		t.Fatalf("도메인 relay 지정: %v", err)
	}
	got, err = s.ResolveRelay(ctx, "kirby.so")
	if err != nil || got.ID != ses.ID {
		t.Fatalf("도메인 지정 해석 실패: %v %+v", err, got)
	}
	got, err = s.ResolveRelay(ctx, "krisam.in") // 미지정 도메인은 여전히 default
	if err != nil || got.ID != resend.ID {
		t.Fatalf("미지정 도메인 default 해석 실패: %v %+v", err, got)
	}
	t.Log("✔ 도메인 지정 > default 우선순위")

	// 4) 비활성 relay는 무시
	ses.Active = false
	if _, err := s.UpdateRelay(ctx, ses); err != nil {
		t.Fatalf("relay 수정: %v", err)
	}
	got, err = s.ResolveRelay(ctx, "kirby.so")
	if err != nil || got.ID != resend.ID {
		t.Fatalf("비활성 무시 실패 (default로 내려와야 함): %v %+v", err, got)
	}
	t.Log("✔ 비활성 relay 무시 → default")

	// 5) UpdateRelay password 빈 문자열 = 기존 값 유지
	resend.Password = ""
	resend.Host = "smtp2.resend.com"
	updated, err := s.UpdateRelay(ctx, resend)
	if err != nil {
		t.Fatalf("relay 수정: %v", err)
	}
	if updated.Password != "re_secret" || updated.Host != "smtp2.resend.com" {
		t.Fatalf("password 유지 실패: %+v", updated)
	}
	t.Log("✔ password 빈 문자열 = 기존 값 유지")

	// 6) default 이관: ses를 default로 → resend는 default 해제
	ses.Active = true
	ses.IsDefault = true
	if _, err := s.UpdateRelay(ctx, ses); err != nil {
		t.Fatalf("default 이관: %v", err)
	}
	relayList, err := s.ListRelay(ctx)
	if err != nil {
		t.Fatalf("relay 목록: %v", err)
	}
	defaultCount := 0
	for _, r := range relayList {
		if r.IsDefault {
			defaultCount++
		}
	}
	if defaultCount != 1 {
		t.Fatalf("default는 하나여야 함, got %d", defaultCount)
	}
	t.Log("✔ default 단일성 (이관 시 기존 해제)")

	// 7) 삭제 → 도메인 relay_id는 SET NULL
	if err := s.DeleteRelay(ctx, ses.ID); err != nil {
		t.Fatalf("relay 삭제: %v", err)
	}
	var relayID *int64
	if err := s.pool.QueryRow(ctx,
		`SELECT relay_id FROM domain WHERE id = $1`, kirbyID).Scan(&relayID); err != nil {
		t.Fatalf("도메인 조회: %v", err)
	}
	if relayID != nil {
		t.Fatalf("삭제 후 relay_id NULL이어야 함, got %v", *relayID)
	}
	t.Log("✔ relay 삭제 → domain.relay_id SET NULL")
}
