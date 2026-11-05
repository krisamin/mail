package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestInternalAdmin — /internal/v1/admin/* serves the /api/admin/* handlers
// behind the service token: absent without a token, 401 without/with a wrong
// token, no account header needed, /me refused, relay password write-only.
func TestInternalAdmin(t *testing.T) {
	off, _ := internalServer(t, "")
	if code, _ := icall(t, off, "GET", "/internal/v1/admin/domain", testServiceToken, "", nil); code != http.StatusNotFound {
		t.Fatalf("token off: want 404, got %d", code)
	}

	srv, _ := internalServer(t, testServiceToken)
	if code, _ := icall(t, srv, "GET", "/internal/v1/admin/domain", "", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("no token: want 401, got %d", code)
	}
	if code, _ := icall(t, srv, "GET", "/internal/v1/admin/domain", "wrong-token", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: want 401, got %d", code)
	}

	// create a domain and read it back through the internal surface
	code, created := ijson(t, srv, "POST", "/internal/v1/admin/domain", "", map[string]string{"name": "admin.test"})
	if code != http.StatusCreated || created["name"] != "admin.test" {
		t.Fatalf("create domain: %d %v", code, created)
	}
	code, raw := icall(t, srv, "GET", "/internal/v1/admin/domain", testServiceToken, "", nil)
	var domainList []map[string]any
	_ = json.Unmarshal(raw, &domainList)
	if code != http.StatusOK || len(domainList) != 1 || domainList[0]["name"] != "admin.test" {
		t.Fatalf("list domain: %d %s", code, raw)
	}

	// path values survive the rewrite: PATCH /domain/{id}
	id := created["id"].(string)
	code, patched := ijson(t, srv, "PATCH", "/internal/v1/admin/domain/"+id, "", map[string]bool{"active": false})
	if code != http.StatusOK || patched["active"] != false {
		t.Fatalf("patch domain: %d %v", code, patched)
	}

	// relay password is write-only here too
	code, raw = icall(t, srv, "POST", "/internal/v1/admin/relay", testServiceToken, "", map[string]any{
		"name": "resend", "host": "smtp.resend.test", "port": 587,
		"username": "resend", "password": "re_secret_value", "isDefault": true,
	})
	if code != http.StatusCreated || strings.Contains(string(raw), "re_secret_value") || !strings.Contains(string(raw), `"hasPassword":true`) {
		t.Fatalf("create relay: %d %s", code, raw)
	}

	// a person's identity has no meaning for a service call
	if code, _ := icall(t, srv, "GET", "/internal/v1/admin/me", testServiceToken, "", nil); code != http.StatusNotFound {
		t.Fatalf("admin/me: want 404, got %d", code)
	}
	// unknown admin path is still a 404 from the admin mux
	if code, _ := icall(t, srv, "GET", "/internal/v1/admin/nothing", testServiceToken, "", nil); code != http.StatusNotFound {
		t.Fatalf("unknown: want 404, got %d", code)
	}
}

// TestSpamSetting — GET /api/admin/spam reports env switches and whether
// rspamd answers its ping.
func TestSpamSetting(t *testing.T) {
	pong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("pong\r\n"))
	}))
	defer pong.Close()

	check := func(sp SpamSetting) map[string]any {
		t.Helper()
		st := (&Server{}).WithSpam(sp)
		rec := httptest.NewRecorder()
		st.handleSpamSetting(rec, httptest.NewRequest("GET", "/api/admin/spam", nil))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}

	out := check(SpamSetting{RspamdURL: pong.URL, Greylist: true, GreylistDelay: 90 * time.Second, DMARCEnforce: true, DNSBLZoneList: []string{"zen.spamhaus.org"}})
	rs := out["rspamd"].(map[string]any)
	if rs["configured"] != true || rs["reachable"] != true {
		t.Fatalf("rspamd up: %v", out)
	}
	if out["greylist"] != true || out["greylistDelaySecond"] != float64(90) || out["dmarcEnforce"] != true || out["source"] != "env" {
		t.Fatalf("switches: %v", out)
	}
	if zl := out["dnsblZoneList"].([]any); len(zl) != 1 || zl[0] != "zen.spamhaus.org" {
		t.Fatalf("zones: %v", out)
	}

	// rspamd that answers something else is not reachable
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("nope")) }))
	defer bad.Close()
	out = check(SpamSetting{RspamdURL: bad.URL})
	if rs := out["rspamd"].(map[string]any); rs["reachable"] != false || rs["error"] == nil {
		t.Fatalf("rspamd bad: %v", out)
	}

	// no rspamd configured, screening off
	out = check(SpamSetting{})
	if rs := out["rspamd"].(map[string]any); rs["configured"] != false {
		t.Fatalf("rspamd off: %v", out)
	}
	if zl := out["dnsblZoneList"].([]any); len(zl) != 0 {
		t.Fatalf("zones off: %v", out)
	}
}
