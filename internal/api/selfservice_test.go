package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Self-service (/api/me/*) integration tests.
// In InsecureSkipVerify mode, the X-Test-Email/X-Test-Groups headers
// simulate "who is logged in" (see auth.go authenticate).

// callAs sends a JSON request as the given identity.
func callAs(t *testing.T, srv *httptest.Server, email, groups, method, path string, body any) (int, map[string]any, []map[string]any) {
	t.Helper()
	var reqBody *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reqBody = bytes.NewReader(b)
	} else {
		reqBody = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, srv.URL+path, reqBody)
	if err != nil {
		t.Fatalf("req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Email", email)
	// even an empty string, when explicit, overrides groups (prevents default admin)
	req.Header["X-Test-Groups"] = []string{groups}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	raw := buf.Bytes()
	var obj map[string]any
	var arr []map[string]any
	if len(raw) > 0 {
		if raw[0] == '[' {
			_ = json.Unmarshal(raw, &arr)
		} else {
			_ = json.Unmarshal(raw, &obj)
		}
	}
	return resp.StatusCode, obj, arr
}

// TestSelfService: own app password lifecycle + boundaries for a regular user (no groups).
func TestSelfService(t *testing.T) {
	srv := testServer(t)

	// seed: domain as admin + two users via JIT provisioning (maro, guest)
	code, dom, _ := call(t, srv, "POST", "/api/admin/domain", map[string]string{"name": "krisam.in"})
	if code != 201 {
		t.Fatalf("domain: %d %v", code, dom)
	}
	for _, name := range []string{"maro", "guest"} {
		if code, u, _ := callAs(t, srv, name+"@krisam.in", "", "POST", "/api/me/provision", nil); code != 200 {
			t.Fatalf("user %s: %d %v", name, code, u)
		}
	}

	// 1) Regular users cannot access the admin API (403)
	code, _, _ = callAs(t, srv, "guest@krisam.in", "", "GET", "/api/admin/domain", nil)
	if code != 403 {
		t.Fatalf("admin access by regular user should be 403: %d", code)
	}
	t.Log("✔ regular user admin API 403")

	// 2) Fetch own account
	code, acc, _ := callAs(t, srv, "guest@krisam.in", "", "GET", "/api/me/account", nil)
	if code != 200 || acc["email"] != "guest@krisam.in" {
		t.Fatalf("own account: %d %v", code, acc)
	}
	t.Log("✔ /api/me/account — sub claim → account mapping")

	// unprovisioned user → 404
	code, _, _ = callAs(t, srv, "nobody@krisam.in", "", "GET", "/api/me/account", nil)
	if code != 404 {
		t.Fatalf("unprovisioned user should be 404: %d", code)
	}
	t.Log("✔ unprovisioned account 404")

	// 3) Issue own app password → list → revoke
	code, pw, _ := callAs(t, srv, "guest@krisam.in", "", "POST", "/api/me/app-password",
		map[string]string{"label": "my phone"})
	if code != 201 || pw["plaintext"] == nil {
		t.Fatalf("issue: %d %v", code, pw)
	}
	guestPwID := pw["appPassword"].(map[string]any)["id"].(string)
	t.Logf("✔ own app password issued: %v", pw["plaintext"])

	code, _, passwordList := callAs(t, srv, "guest@krisam.in", "", "GET", "/api/me/app-password", nil)
	if code != 200 || len(passwordList) != 1 {
		t.Fatalf("list: %d %v", code, passwordList)
	}

	// 4) IDOR prevention — maro tries to revoke guest's password → 404
	code, _, _ = callAs(t, srv, "maro@krisam.in", "", "DELETE",
		fmt.Sprintf("/api/me/app-password/%s", guestPwID), nil)
	if code != 404 {
		t.Fatalf("revoking someone else's password should be 404 (IDOR): %d", code)
	}
	t.Log("✔ IDOR prevention — revoking someone else's app password 404")

	// own revoke succeeds
	code, _, _ = callAs(t, srv, "guest@krisam.in", "", "DELETE",
		fmt.Sprintf("/api/me/app-password/%s", guestPwID), nil)
	if code != 204 {
		t.Fatalf("own revoke: %d", code)
	}
	code, _, passwordList = callAs(t, srv, "guest@krisam.in", "", "GET", "/api/me/app-password", nil)
	if code != 200 || len(passwordList) != 1 || passwordList[0]["revoked"] != true {
		t.Fatalf("revoke applied: %d %v", code, passwordList)
	}
	t.Log("✔ own revoke 204 + applied")

	// 5) Email case normalization (Guest@Krisam.IN → guest account)
	code, acc, _ = callAs(t, srv, "Guest@Krisam.IN", "", "GET", "/api/me/account", nil)
	if code != 200 || acc["email"] != "guest@krisam.in" {
		t.Fatalf("case normalization: %d %v", code, acc)
	}
	t.Log("✔ email case normalization")
}
