package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// DNS verification (0005) — actually queries the domain's mail-related DNS
// records and shows ✅/⚠️/❌ badges on the admin screen. Lookup failures
// (not NXDOMAIN) are classified as error status, not missing.
//
// Items: MX (our server), SPF (TXT v=spf1), DKIM (<selector>._domainkey — including
// whether it matches the DB key), DMARC (_dmarc TXT).
// Autodiscovery: SRV (_imaps/_submissions/_submission — RFC 6186/8314),
// autoconfig (Thunderbird XML autoconfiguration host).

type dnsCheckDTO struct {
	// Status: "ok" | "warn" | "missing"
	Status string `json:"status"`
	// Found is the value actually found in DNS (empty string if none).
	Found string `json:"found"`
	// Expected is the recommended/expected value — so the admin can copy-paste it.
	Expected string `json:"expected,omitempty"`
	// Note is the status description.
	Note string `json:"note,omitempty"`
}

type dnsVerifyDTO struct {
	Domain string      `json:"domain"`
	MX     dnsCheckDTO `json:"mx"`
	SPF    dnsCheckDTO `json:"spf"`
	DKIM   dnsCheckDTO `json:"dkim"`
	DMARC  dnsCheckDTO `json:"dmarc"`
	// Autodiscovery — records that let a client find the server from just an email address.
	// The imaps/submissions in the field names are not plurals but the DNS service labels as-is
	// (_imaps._tcp = IMAP over TLS 993, _submissions._tcp = implicit TLS 465,
	//  _submission._tcp = STARTTLS 587).
	SRVImaps       dnsCheckDTO `json:"srvImaps"`
	SRVSubmissions dnsCheckDTO `json:"srvSubmissions"`
	SRVSubmission  dnsCheckDTO `json:"srvSubmission"`
	Autoconfig     dnsCheckDTO `json:"autoconfig"`
}

// handleVerifyDomainDNS performs live DNS lookups for the domain's DNS status.
func (s *Server) handleVerifyDomainDNS(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	// Load the domain (DKIM key needed too — find it via ListDomain)
	domainList, err := s.store.ListDomain(r.Context())
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	var name, dkimSelector, dkimKey string
	for _, d := range domainList {
		if d.ID == id {
			name, dkimSelector, dkimKey = d.Name, d.DKIMSelector, d.DKIMPrivateKey
			break
		}
	}
	if name == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	out := dnsVerifyDTO{Domain: name}
	out.MX = checkMX(ctx, name, s.hostname)
	out.SPF = checkSPF(ctx, name)
	out.DKIM = checkDKIM(ctx, name, dkimSelector, dkimKey)
	out.DMARC = checkDMARC(ctx, name)
	out.SRVImaps = checkSRV(ctx, "imaps", name, s.hostname, 993)
	out.SRVSubmissions = checkSRV(ctx, "submissions", name, s.hostname, 465)
	out.SRVSubmission = checkSRV(ctx, "submission", name, s.hostname, 587)
	out.Autoconfig = checkAutoconfigDNS(ctx, name, s.hostname)
	writeJSON(w, http.StatusOK, out)
}

// resolver queries public DNS (1.1.1.1) directly.
// The local resolver may be split DNS (internal domain overrides), but the
// purpose of this verification is "the DNS the outside world sees", so going
// through a public resolver is correct.
var resolver = &net.Resolver{
	PreferGo: true,
	Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		d := net.Dialer{Timeout: 5 * time.Second}
		return d.DialContext(ctx, network, "1.1.1.1:53")
	},
}

// dnsUnavailable detects lookup failures that are not "record absent (NXDOMAIN/NODATA)".
// Misjudging a transient DNS outage as missing would send the admin off to
// reconfigure perfectly fine records — classify it as error status instead.
func dnsUnavailable(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return !dnsErr.IsNotFound
	}
	return true
}

// dnsErrorCheck is the shared DTO for lookup failures.
func dnsErrorCheck(name string) dnsCheckDTO {
	return dnsCheckDTO{
		Status: "error",
		Note:   name + " lookup failed — no DNS response (not the same as record absent, retry shortly)",
	}
}

func lookupTXTJoined(ctx context.Context, name string) ([]string, error) {
	txtList, err := resolver.LookupTXT(ctx, name)
	if dnsUnavailable(err) {
		return nil, err
	}
	return txtList, nil
}

func checkMX(ctx context.Context, domain, expectedHost string) dnsCheckDTO {
	mxList, err := resolver.LookupMX(ctx, domain)
	if dnsUnavailable(err) {
		return dnsErrorCheck(domain + " MX")
	}
	if len(mxList) == 0 {
		return dnsCheckDTO{
			Status: "missing", Expected: expectedHost,
			Note: "no MX record — receiving mail requires an MX pointing at this server",
		}
	}
	foundList := make([]string, 0, len(mxList))
	match := false
	for _, mx := range mxList {
		host := strings.TrimSuffix(mx.Host, ".")
		foundList = append(foundList, fmt.Sprintf("%s (pref %d)", host, mx.Pref))
		if expectedHost != "" && strings.EqualFold(host, expectedHost) {
			match = true
		}
	}
	found := strings.Join(foundList, ", ")
	if expectedHost == "" || match {
		return dnsCheckDTO{Status: "ok", Found: found}
	}
	return dnsCheckDTO{
		Status: "warn", Found: found, Expected: expectedHost,
		Note: "MX does not point at this server (" + expectedHost + ")",
	}
}

func checkSPF(ctx context.Context, domain string) dnsCheckDTO {
	txtList, err := lookupTXTJoined(ctx, domain)
	if err != nil {
		return dnsErrorCheck(domain + " TXT")
	}
	for _, txt := range txtList {
		if strings.HasPrefix(strings.ToLower(txt), "v=spf1") {
			return dnsCheckDTO{Status: "ok", Found: txt}
		}
	}
	return dnsCheckDTO{
		Status: "missing", Expected: "v=spf1 ... ~all",
		Note: "no SPF — register the include provided by your relay provider",
	}
}

func checkDKIM(ctx context.Context, domain, selector, privateKeyPEM string) dnsCheckDTO {
	if selector == "" {
		return dnsCheckDTO{Status: "warn", Note: "DKIM key not generated — create one in domain management"}
	}
	name := selector + "._domainkey." + domain
	txtList, err := lookupTXTJoined(ctx, name)
	if err != nil {
		return dnsErrorCheck(name + " TXT")
	}
	if len(txtList) == 0 {
		expected := ""
		if txt, err := dkimPublicTXT(privateKeyPEM); err == nil {
			expected = txt
		}
		return dnsCheckDTO{
			Status: "missing", Expected: expected,
			Note: name + " TXT missing",
		}
	}
	found := strings.Join(txtList, "")
	expected, err := dkimPublicTXT(privateKeyPEM)
	if err == nil {
		if extractDKIMKey(found) == extractDKIMKey(expected) {
			return dnsCheckDTO{Status: "ok", Found: found}
		}
		return dnsCheckDTO{
			Status: "warn", Found: found, Expected: expected,
			Note: "public key in DNS differs from the server key — signature verification will fail",
		}
	}
	return dnsCheckDTO{Status: "ok", Found: found, Note: "presence confirmed (key comparison not possible)"}
}

// extractDKIMKey extracts only the p= value from the TXT (for comparison ignoring whitespace/quote splits).
func extractDKIMKey(txt string) string {
	txt = strings.ReplaceAll(txt, " ", "")
	txt = strings.ReplaceAll(txt, "\"", "")
	if i := strings.Index(txt, "p="); i >= 0 {
		rest := txt[i+2:]
		if j := strings.Index(rest, ";"); j >= 0 {
			return rest[:j]
		}
		return rest
	}
	return ""
}

func checkDMARC(ctx context.Context, domain string) dnsCheckDTO {
	name := "_dmarc." + domain
	txtList, err := lookupTXTJoined(ctx, name)
	if err != nil {
		return dnsErrorCheck(name + " TXT")
	}
	for _, txt := range txtList {
		if strings.HasPrefix(strings.ToLower(txt), "v=dmarc1") {
			return dnsCheckDTO{Status: "ok", Found: txt}
		}
	}
	return dnsCheckDTO{
		Status:   "missing",
		Expected: "v=DMARC1; p=none; rua=mailto:postmaster@" + domain,
		Note:     name + " TXT missing",
	}
}

// checkSRV checks the RFC 6186/8314 client autodiscovery SRV records.
// service: "imaps"(993) | "submissions"(465) | "submission"(587).
// Mail clients use these records on the email domain to find the server to
// connect to — without them they fall back to guesses like imap.<domain>
// and may end up connecting to the wrong place.
func checkSRV(ctx context.Context, service, domain, expectedHost string, expectedPort uint16) dnsCheckDTO {
	recordName := fmt.Sprintf("_%s._tcp.%s", service, domain)
	expected := fmt.Sprintf("%s IN SRV 0 1 %d %s.", recordName, expectedPort, expectedHost)
	if expectedHost == "" {
		expected = ""
	}

	_, srvList, err := resolver.LookupSRV(ctx, service, "tcp", domain)
	if dnsUnavailable(err) {
		return dnsErrorCheck(recordName + " SRV")
	}
	if len(srvList) == 0 {
		return dnsCheckDTO{
			Status: "missing", Expected: expected,
			Note: recordName + " SRV missing — client autoconfiguration cannot find the server",
		}
	}
	foundList := make([]string, 0, len(srvList))
	match := false
	for _, srv := range srvList {
		host := strings.TrimSuffix(srv.Target, ".")
		foundList = append(foundList, fmt.Sprintf("%s:%d (prio %d)", host, srv.Port, srv.Priority))
		if expectedHost != "" && strings.EqualFold(host, expectedHost) && srv.Port == expectedPort {
			match = true
		}
	}
	found := strings.Join(foundList, ", ")
	if expectedHost == "" || match {
		return dnsCheckDTO{Status: "ok", Found: found}
	}
	return dnsCheckDTO{
		Status: "warn", Found: found, Expected: expected,
		Note: fmt.Sprintf("SRV does not point at this server (%s:%d)", expectedHost, expectedPort),
	}
}

// checkAutoconfigDNS checks the Thunderbird-style autoconfiguration host.
// Clients look for http(s)://autoconfig.<domain>/mail/config-v1.1.xml first,
// so autoconfig.<domain> must resolve to this server (web) for the XML to be served.
// A/AAAA/CNAME — anything is ok as long as it ultimately resolves to our server host.
func checkAutoconfigDNS(ctx context.Context, domain, expectedHost string) dnsCheckDTO {
	recordName := "autoconfig." + domain
	expected := recordName + " IN CNAME " + expectedHost + "."
	if expectedHost == "" {
		expected = ""
	}

	// First check whether a CNAME points straight at the expected host (the clearest signal).
	if cname, err := resolver.LookupCNAME(ctx, recordName); err == nil && expectedHost != "" {
		if strings.EqualFold(strings.TrimSuffix(cname, "."), expectedHost) {
			return dnsCheckDTO{Status: "ok", Found: recordName + " → " + expectedHost}
		}
	}

	addrList, err := resolver.LookupHost(ctx, recordName)
	if dnsUnavailable(err) {
		return dnsErrorCheck(recordName)
	}
	if len(addrList) == 0 {
		return dnsCheckDTO{
			Status: "missing", Expected: expected,
			Note: recordName + " missing — Thunderbird autoconfiguration not possible",
		}
	}
	found := recordName + " → " + strings.Join(addrList, ", ")
	// IP comparison: ok if it overlaps with the expected host's IP set (an A record without a CNAME also counts).
	if expectedHost != "" {
		if expectedAddrList, err := resolver.LookupHost(ctx, expectedHost); err == nil {
			expectedSet := map[string]bool{}
			for _, a := range expectedAddrList {
				expectedSet[a] = true
			}
			for _, a := range addrList {
				if expectedSet[a] {
					return dnsCheckDTO{Status: "ok", Found: found}
				}
			}
		}
		return dnsCheckDTO{
			Status: "warn", Found: found, Expected: expected,
			Note: "autoconfig does not resolve to this server (" + expectedHost + ")",
		}
	}
	return dnsCheckDTO{Status: "ok", Found: found}
}
