package api

import (
	"encoding/xml"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/krisamin/mail/internal/store"
)

// Thunderbird autoconfig (config-v1.1) — XML that lets a client find the
// IMAP/SMTP servers from just an email address. No auth (config values only,
// no secrets).
//
// Client lookup order (Thunderbird family):
//   1. https://autoconfig.<domain>/mail/config-v1.1.xml?emailaddress=...
//   2. http://autoconfig.<domain>/mail/config-v1.1.xml (converges via http→https redirect)
//   3. https://<domain>/.well-known/autoconfig/mail/config-v1.1.xml
// → autoconfig.<domain> must point at this server (the web gateway).
//
// Domain resolution: emailaddress query → Host header (autoconfig. prefix stripped).
// Only DB-registered domains get a response (unregistered = 404 — not our domain).

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

// handleAutoconfigXML serves the Thunderbird autoconfig XML.
func (s *Server) handleAutoconfigXML(w http.ResponseWriter, r *http.Request) {
	if s.hostname == "" {
		writeError(w, http.StatusServiceUnavailable, "server hostname not configured")
		return
	}

	// domain resolution — emailaddress query first, else from the Host header.
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

	// registered active domains only — serving config for a domain we don't
	// handle sends clients at this server with the wrong accounts.
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
