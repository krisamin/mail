package api

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/krisamin/mail/internal/store/postgres"
)

// TestSystemCheck: /api/admin/system — DB/queue/port check response shape.
// Ports show as closed (open=false) since the tests have no real listeners.
func TestSystemCheck(t *testing.T) {
	dsn := os.Getenv("MAIL_TEST_DSN")
	if dsn == "" {
		t.Skip("MAIL_TEST_DSN not set — skipping integration tests")
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
		{Name: "imap", Addr: ":59999", Kind: "imap", Check: true}, // closed port
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	code, body, _ := call(t, srv, "GET", "/api/admin/system", nil)
	if code != 200 {
		t.Fatalf("system: %d %v", code, body)
	}
	db, ok := body["db"].(map[string]any)
	if !ok || db["ok"] != true {
		t.Fatalf("db should be ok: %v", body["db"])
	}
	queue, ok := body["queue"].(map[string]any)
	if !ok || queue["ok"] != true {
		t.Fatalf("queue should be ok: %v", body["queue"])
	}
	portList, ok := body["listener"].([]any)
	if !ok || len(portList) != 1 {
		t.Fatalf("should have exactly 1 listener: %v", body["listener"])
	}
	p := portList[0].(map[string]any)
	if p["name"] != "imap" || p["open"] != false {
		t.Fatalf("closed port should be open=false: %v", p)
	}
	if body["uptime"] == "" {
		t.Fatal("uptime should be present")
	}
	t.Log("✔ system check (db/queue ok, closed port detected)")

	// regular users cannot access (admin-only middleware)
	code, _, _ = callAs(t, srv, "user@krisam.in", "", "GET", "/api/admin/system", nil)
	if code != 403 {
		t.Fatalf("regular user should be 403: %d", code)
	}
	t.Log("✔ admin only")
}
