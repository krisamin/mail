package api

import (
	"encoding/xml"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/krisamin/mail/internal/store"
)

// Thunderbird autoconfig (config-v1.1) — 클라이언트가 이메일 주소만으로
// IMAP/SMTP 서버를 찾게 하는 XML. 인증 불필요 (설정값만 노출, 비밀 없음).
//
// 클라이언트 조회 순서 (Thunderbird 계열):
//   1. https://autoconfig.<도메인>/mail/config-v1.1.xml?emailaddress=...
//   2. http://autoconfig.<도메인>/mail/config-v1.1.xml (http→https 리다이렉트로 수렴)
//   3. https://<도메인>/.well-known/autoconfig/mail/config-v1.1.xml
// → autoconfig.<도메인>이 이 서버(웹 게이트웨이)로 향해야 한다.
//
// 도메인 결정: emailaddress 쿼리 → Host 헤더(autoconfig. prefix 제거).
// DB에 등록된 도메인만 응답 (미등록 = 404 — 우리가 안 받는 도메인).

type autoconfigIncoming struct {
	XMLName        xml.Name `xml:"incomingServer"`
	Type           string   `xml:"type,attr"`
	Hostname       string   `xml:"hostname"`
	Port           int      `xml:"port"`
	SocketType     string   `xml:"socketType"`
	Authentication string   `xml:"authentication"`
	Username       string   `xml:"username"`
}

type autoconfigOutgoing struct {
	XMLName        xml.Name `xml:"outgoingServer"`
	Type           string   `xml:"type,attr"`
	Hostname       string   `xml:"hostname"`
	Port           int      `xml:"port"`
	SocketType     string   `xml:"socketType"`
	Authentication string   `xml:"authentication"`
	Username       string   `xml:"username"`
}

type autoconfigProvider struct {
	XMLName          xml.Name `xml:"emailProvider"`
	ID               string   `xml:"id,attr"`
	Domain           string   `xml:"domain"`
	DisplayName      string   `xml:"displayName"`
	DisplayShortName string   `xml:"displayShortName"`
	Incoming         []autoconfigIncoming
	Outgoing         []autoconfigOutgoing
}

type autoconfigRoot struct {
	XMLName  xml.Name `xml:"clientConfig"`
	Version  string   `xml:"version,attr"`
	Provider autoconfigProvider
}

// handleAutoconfigXML은 Thunderbird autoconfig XML을 서빙한다.
func (s *Server) handleAutoconfigXML(w http.ResponseWriter, r *http.Request) {
	if s.hostname == "" {
		writeError(w, http.StatusServiceUnavailable, "server hostname not configured")
		return
	}

	// 도메인 결정 — emailaddress 쿼리 우선, 없으면 Host 헤더에서.
	domain := ""
	if email := r.URL.Query().Get("emailaddress"); email != "" {
		if i := strings.LastIndex(email, "@"); i >= 0 {
			domain = strings.ToLower(strings.TrimSpace(email[i+1:]))
		}
	}
	if domain == "" {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.ToLower(host)
		if after, ok := strings.CutPrefix(host, "autoconfig."); ok {
			domain = after
		}
	}
	if domain == "" {
		writeError(w, http.StatusBadRequest, "cannot determine mail domain (use ?emailaddress= or autoconfig.<domain> host)")
		return
	}

	// 등록된 활성 도메인만 — 우리가 안 받는 도메인의 설정을 주면
	// 클라이언트가 엉뚱한 계정으로 이 서버에 붙으려 든다.
	if _, err := s.store.FindDomain(r.Context(), domain); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "domain not served here: "+domain)
			return
		}
		mapStoreErr(w, err)
		return
	}

	out := autoconfigRoot{
		Version: "1.1",
		Provider: autoconfigProvider{
			ID:               domain,
			Domain:           domain,
			DisplayName:      "mail (" + domain + ")",
			DisplayShortName: "mail",
			Incoming: []autoconfigIncoming{{
				Type: "imap", Hostname: s.hostname, Port: 993,
				SocketType: "SSL", Authentication: "password-cleartext",
				Username: "%EMAILADDRESS%",
			}},
			Outgoing: []autoconfigOutgoing{
				{
					Type: "smtp", Hostname: s.hostname, Port: 465,
					SocketType: "SSL", Authentication: "password-cleartext",
					Username: "%EMAILADDRESS%",
				},
				{
					Type: "smtp", Hostname: s.hostname, Port: 587,
					SocketType: "STARTTLS", Authentication: "password-cleartext",
					Username: "%EMAILADDRESS%",
				},
			},
		},
	}

	body, err := xml.MarshalIndent(out, "", "  ")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "xml marshal failed")
		return
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(body)
}
