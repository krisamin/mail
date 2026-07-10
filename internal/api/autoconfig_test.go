package api

import (
	"encoding/xml"
	"io"
	"net/http"
	"strings"
	"testing"
)

// autoconfig XML — domain resolution (query/Host), registered-domain gate, server settings.
func TestAutoconfigXML(t *testing.T) {
	srv := testServer(t)

	// Register domain.
	status, _, _ := call(t, srv, "POST", "/api/admin/domain", map[string]string{"name": "kirby.so"})
	if status != http.StatusCreated {
		t.Fatalf("create domain: status=%d", status)
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

	// Domain resolution via the emailaddress query.
	status, body := get("/mail/config-v1.1.xml?emailaddress=krisamin@kirby.so", "")
	if status != http.StatusOK {
		t.Fatalf("autoconfig(query): status=%d body=%s", status, body)
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
		t.Fatalf("XML parse: %v\n%s", err, body)
	}
	if cfg.Provider.Domain != "kirby.so" {
		t.Fatalf("domain=%q", cfg.Provider.Domain)
	}
	if cfg.Provider.Incoming.Hostname != "mail.example.test" || cfg.Provider.Incoming.Port != 993 {
		t.Fatalf("incoming=%+v", cfg.Provider.Incoming)
	}
	if !strings.Contains(body, "%EMAILADDRESS%") {
		t.Fatalf("username placeholder missing:\n%s", body)
	}

	// Domain resolution via the Host header (autoconfig. prefix) + .well-known path.
	status, body = get("/.well-known/autoconfig/mail/config-v1.1.xml", "autoconfig.kirby.so")
	if status != http.StatusOK || !strings.Contains(body, "<domain>kirby.so</domain>") {
		t.Fatalf("autoconfig(Host): status=%d body=%s", status, body)
	}

	// Unregistered domain = 404.
	status, _ = get("/mail/config-v1.1.xml?emailaddress=x@unknown.example", "")
	if status != http.StatusNotFound {
		t.Fatalf("unregistered domain but status=%d", status)
	}

	// Domain cannot be resolved = 400.
	status, _ = get("/mail/config-v1.1.xml", "")
	if status != http.StatusBadRequest {
		t.Fatalf("domain unresolvable but status=%d", status)
	}
}
