package spam

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// rspamd scanner tests against a fake checkv2 endpoint.

func fakeRspamd(t *testing.T, action string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/checkv2" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("IP") == "" || r.Header.Get("From") == "" {
			t.Errorf("context headers missing: IP=%q From=%q", r.Header.Get("IP"), r.Header.Get("From"))
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"action": action, "score": 7.5, "required_score": 15.0,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func scanOnce(t *testing.T, srv *httptest.Server) (ScanResult, error) {
	t.Helper()
	sc := NewScanner(srv.URL, "")
	return sc.Scan(context.Background(), []byte("Subject: x\r\n\r\nbody"),
		net.ParseIP("1.2.3.4"), "mail.example.com", "a@b.c", []string{"d@e.f"})
}

func TestScanActionMapping(t *testing.T) {
	caseList := []struct {
		raw  string
		want ScanAction
	}{
		{"no action", ScanPass},
		{"reject", ScanReject},
		{"add header", ScanJunk},
		{"rewrite subject", ScanJunk},
		{"greylist", ScanJunk},
		{"soft reject", ScanJunk},
	}
	for _, c := range caseList {
		result, err := scanOnce(t, fakeRspamd(t, c.raw, http.StatusOK))
		if err != nil {
			t.Fatalf("%s: %v", c.raw, err)
		}
		if result.Action != c.want {
			t.Errorf("%s: got %v want %v", c.raw, result.Action, c.want)
		}
	}
	t.Log("✔ action mapping")
}

func TestScanFailOpen(t *testing.T) {
	// HTTP 500 → pass + error
	result, err := scanOnce(t, fakeRspamd(t, "", http.StatusInternalServerError))
	if err == nil || result.Action != ScanPass {
		t.Fatalf("500 should fail-open: %v %v", result, err)
	}
	// unreachable server → pass + error
	sc := NewScanner("http://127.0.0.1:1", "")
	result, err = sc.Scan(context.Background(), []byte("x"), nil, "", "", nil)
	if err == nil || result.Action != ScanPass {
		t.Fatalf("unreachable should fail-open: %v %v", result, err)
	}
	t.Log("✔ fail-open on scanner errors")
}
