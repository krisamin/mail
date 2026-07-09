package api

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/krisamin/mail/internal/store/postgres"
)

// TestSystemCheck: /api/admin/system — DB/큐/포트 점검 응답 형태.
// 포트는 테스트에서 실제 리스너가 없으므로 닫힘으로 나온다 (open=false).
func TestSystemCheck(t *testing.T) {
	dsn := os.Getenv("MAIL_TEST_DSN")
	if dsn == "" {
		t.Skip("MAIL_TEST_DSN 미설정 — 통합 테스트 skip")
	}
	st, err := postgres.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(st.Close)

	auth, err := NewAuthenticator(context.Background(), AuthConfig{
		AdminGroup: "mail-admin", InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	handler := NewServer(st, auth).WithSystemPort([]SystemPort{
		{Name: "imap", Addr: ":59999", Kind: "imap", Check: true}, // 닫힌 포트
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	code, body, _ := call(t, srv, "GET", "/api/admin/system", nil)
	if code != 200 {
		t.Fatalf("system: %d %v", code, body)
	}
	db, ok := body["db"].(map[string]any)
	if !ok || db["ok"] != true {
		t.Fatalf("db ok여야: %v", body["db"])
	}
	queue, ok := body["queue"].(map[string]any)
	if !ok || queue["ok"] != true {
		t.Fatalf("queue ok여야: %v", body["queue"])
	}
	portList, ok := body["port"].([]any)
	if !ok || len(portList) != 1 {
		t.Fatalf("port 1개여야: %v", body["port"])
	}
	p := portList[0].(map[string]any)
	if p["name"] != "imap" || p["open"] != false {
		t.Fatalf("닫힌 포트는 open=false여야: %v", p)
	}
	if body["uptime"] == "" {
		t.Fatal("uptime 있어야")
	}
	t.Log("✔ 시스템 점검 (db/queue ok, 닫힌 포트 감지)")

	// 일반 유저는 접근 불가 (admin 전용 미들웨어)
	code, _, _ = callAs(t, srv, "user@krisam.in", "", "GET", "/api/admin/system", nil)
	if code != 403 {
		t.Fatalf("일반 유저는 403이어야: %d", code)
	}
	t.Log("✔ admin 전용")
}
