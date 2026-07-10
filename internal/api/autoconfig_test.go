package api

import (
	"encoding/xml"
	"io"
	"net/http"
	"strings"
	"testing"
)

// autoconfig XML — 도메인 결정(쿼리/Host), 등록 도메인 게이트, 서버 설정값.
func TestAutoconfigXML(t *testing.T) {
	srv := testServer(t)

	// 도메인 등록.
	status, _, _ := call(t, srv, "POST", "/api/admin/domain", map[string]string{"name": "kirby.so"})
	if status != http.StatusCreated {
		t.Fatalf("도메인 생성: status=%d", status)
	}

	get := func(path, host string) (int, string) {
		req, err := http.NewRequest("GET", srv.URL+path, nil)
		if err != nil {
			t.Fatalf("req: %v", err)
		}
		if host != "" {
			req.Host = host
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	// emailaddress 쿼리로 도메인 결정.
	status, body := get("/mail/config-v1.1.xml?emailaddress=krisamin@kirby.so", "")
	if status != http.StatusOK {
		t.Fatalf("autoconfig(쿼리): status=%d body=%s", status, body)
	}
	var cfg struct {
		XMLName  xml.Name `xml:"clientConfig"`
		Provider struct {
			Domain   string `xml:"domain"`
			Incoming struct {
				Hostname string `xml:"hostname"`
				Port     int    `xml:"port"`
			} `xml:"incomingServer"`
		} `xml:"emailProvider"`
	}
	if err := xml.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatalf("XML 파싱: %v\n%s", err, body)
	}
	if cfg.Provider.Domain != "kirby.so" {
		t.Fatalf("domain=%q", cfg.Provider.Domain)
	}
	if cfg.Provider.Incoming.Hostname != "mail.example.test" || cfg.Provider.Incoming.Port != 993 {
		t.Fatalf("incoming=%+v", cfg.Provider.Incoming)
	}
	if !strings.Contains(body, "%EMAILADDRESS%") {
		t.Fatalf("username 플레이스홀더 없음:\n%s", body)
	}

	// Host 헤더(autoconfig. prefix)로 도메인 결정 + .well-known 경로.
	status, body = get("/.well-known/autoconfig/mail/config-v1.1.xml", "autoconfig.kirby.so")
	if status != http.StatusOK || !strings.Contains(body, "<domain>kirby.so</domain>") {
		t.Fatalf("autoconfig(Host): status=%d body=%s", status, body)
	}

	// 미등록 도메인 = 404.
	status, _ = get("/mail/config-v1.1.xml?emailaddress=x@unknown.example", "")
	if status != http.StatusNotFound {
		t.Fatalf("미등록 도메인인데 status=%d", status)
	}

	// 도메인 결정 불가 = 400.
	status, _ = get("/mail/config-v1.1.xml", "")
	if status != http.StatusBadRequest {
		t.Fatalf("도메인 결정 불가인데 status=%d", status)
	}
}
