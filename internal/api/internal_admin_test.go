package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/krisamin/mail/internal/spam"
	"github.com/krisamin/mail/internal/store"
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

	// the domain shows the relay assigned to it (null = default)
	var relay map[string]any
	_ = json.Unmarshal(raw, &relay)
	if code, _ := ijson(t, srv, "PUT", "/internal/v1/admin/domain/"+id+"/relay", "", map[string]any{"relayId": relay["id"]}); code != http.StatusOK {
		t.Fatalf("set domain relay: %d", code)
	}
	_, raw = icall(t, srv, "GET", "/internal/v1/admin/domain", testServiceToken, "", nil)
	_ = json.Unmarshal(raw, &domainList)
	if domainList[0]["relayId"] != relay["id"] {
		t.Fatalf("domain relayId: %s", raw)
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

// TestSpamPolicy: GET reports the env default with rspamd's ping; PUT saves a
// whole policy (validated) that GET then returns as source "setting"; DELETE
// goes back to env.
func TestSpamPolicy(t *testing.T) {
	pong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("pong\r\n"))
	}))
	defer pong.Close()

	srv, st := internalServer(t, testServiceToken)
	_ = srv
	src := spam.NewSource(spam.Policy{
		DMARCEnforce: true, DNSBLZoneList: []string{"zen.spamhaus.org"},
		Greylist: true, GreylistDelaySecond: 60, Rspamd: true,
	}, spam.NewScanner(pong.URL, ""), func(ctx context.Context) (string, bool, error) {
		raw, err := st.GetSetting(ctx, spam.PolicySettingKey)
		if errors.Is(err, store.ErrNotFound) {
			return "", false, nil
		}
		return raw, err == nil, err
	})
	auth, _ := NewAuthenticator(context.Background(), AuthConfig{AdminGroup: "mail-admin", InsecureSkipVerify: true})
	api := httptest.NewServer(NewServer(st, auth).WithInternal(testServiceToken).
		WithSpam(SpamSetting{RspamdURL: pong.URL, Source: src}))
	defer api.Close()

	code, out := ijson(t, api, "GET", "/internal/v1/admin/spam", "", nil)
	if code != 200 || out["source"] != "env" || out["rspamd"].(map[string]any)["reachable"] != true {
		t.Fatalf("get env: %d %v", code, out)
	}
	if p := out["policy"].(map[string]any); p["greylist"] != true || p["greylistDelaySecond"] != float64(60) {
		t.Fatalf("env policy: %v", p)
	}

	for _, bad := range []map[string]any{
		{"greylistDelaySecond": 0},
		{"greylistDelaySecond": 60, "dnsblZoneList": []string{"not a zone"}},
		{"greylistDelaySecond": 60, "junkScore": 9, "rejectScore": 5},
	} {
		if code, _ := ijson(t, api, "PUT", "/internal/v1/admin/spam", "", bad); code != 400 {
			t.Fatalf("bad %v: want 400, got %d", bad, code)
		}
	}

	code, out = ijson(t, api, "PUT", "/internal/v1/admin/spam", "", map[string]any{
		"dmarcEnforce": false, "dnsblZoneList": []string{"Zen.Spamhaus.org", "bl.spamcop.net", "zen.spamhaus.org"},
		"greylist": false, "greylistDelaySecond": 120, "rspamd": true, "junkScore": 5, "rejectScore": 12,
	})
	if code != 200 || out["source"] != "setting" {
		t.Fatalf("put: %d %v", code, out)
	}
	snap, saved := src.Current(context.Background())
	if !saved || snap.Greylist || snap.EnforceDMARC || snap.GreylistDelay != 120*time.Second ||
		len(snap.Policy.DNSBLZoneList) != 2 || snap.Policy.DNSBLZoneList[0] != "zen.spamhaus.org" ||
		snap.Scanner == nil || *snap.Policy.RejectScore != 12 {
		t.Fatalf("snapshot after put: %+v", snap)
	}
	raw, err := st.GetSetting(context.Background(), spam.PolicySettingKey)
	if err != nil || !strings.Contains(raw, `"junkScore":5`) {
		t.Fatalf("stored: %q %v", raw, err)
	}

	code, out = ijson(t, api, "DELETE", "/internal/v1/admin/spam", "", nil)
	if code != 200 || out["source"] != "env" {
		t.Fatalf("delete: %d %v", code, out)
	}
	if snap, saved := src.Current(context.Background()); saved || !snap.Greylist {
		t.Fatalf("after reset: %+v", snap)
	}
}
