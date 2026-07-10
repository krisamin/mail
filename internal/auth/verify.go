package auth

import (
	"bytes"
	"fmt"
	"net"
	"strings"

	"blitiri.com.ar/go/spf"
	"github.com/emersion/go-msgauth/authres"
	"github.com/emersion/go-msgauth/dkim"
	"github.com/emersion/go-msgauth/dmarc"
)

// VerifyOptions holds the inputs required for inbound verification.
type VerifyOptions struct {
	// RemoteIP is the SMTP client's IP (SPF target).
	RemoteIP net.IP
	// HeloName is the EHLO/HELO name.
	HeloName string
	// EnvelopeFrom is the MAIL FROM address.
	EnvelopeFrom string
	// Hostname is the authserv-id for Authentication-Results.
	Hostname string

	// LookupTXT is a DNS override for tests (nil uses real DNS).
	LookupTXT func(domain string) ([]string, error)
	// SPFResolver is for tests (nil uses real DNS).
	SPFResolver spf.DNSResolver
}

// VerifyResult summarizes the inbound verification result.
type VerifyResult struct {
	// Header is the Authentication-Results header to prepend to the message (CRLF included).
	Header []byte
	// SPFPass / DKIMPass / DMARCPass — summary for policy decisions.
	SPFPass   bool
	DKIMPass  bool
	DMARCPass bool
	// DMARCEvaluated indicates the sending domain has a DMARC record and an evaluation took place.
	DMARCEvaluated bool
	// DMARCPolicy is the policy published by the sending domain ("none"|"quarantine"|"reject").
	// Only meaningful when DMARCEvaluated is true.
	DMARCPolicy string
	// FromParsed indicates domain extraction from the From header succeeded. If false,
	// the From header is missing or malformed — meaning DMARC evaluation itself was
	// impossible, so in enforcement mode this should be treated as a suspicious signal
	// rather than fail-open (RFC 7489 §6.6.1).
	FromParsed bool
}

// VerifyInbound verifies SPF/DKIM/DMARC of inbound mail and builds the
// Authentication-Results header. Verification failures are not errors —
// they are only recorded in the result (rejection policy is Phase 4).
func VerifyInbound(raw []byte, opts VerifyOptions) *VerifyResult {
	var resultList []authres.Result
	res := &VerifyResult{}

	// ── SPF (RFC 7208) ──────────────────────────────────────
	var spfOptionList []spf.Option
	if opts.SPFResolver != nil {
		spfOptionList = append(spfOptionList, spf.WithResolver(opts.SPFResolver))
	}
	spfResult, _ := spf.CheckHostWithSender(opts.RemoteIP, opts.HeloName, opts.EnvelopeFrom, spfOptionList...)
	res.SPFPass = spfResult == spf.Pass
	resultList = append(resultList, &authres.SPFResult{
		Value: authresValue(string(spfResult)),
		From:  opts.EnvelopeFrom,
		Helo:  opts.HeloName,
	})

	// ── DKIM (RFC 6376) ─────────────────────────────────────
	var dkimOpts *dkim.VerifyOptions
	if opts.LookupTXT != nil {
		dkimOpts = &dkim.VerifyOptions{LookupTXT: opts.LookupTXT}
	}
	verificationList, err := dkim.VerifyWithOptions(bytes.NewReader(raw), dkimOpts)
	var dkimDomainList []string // signature domains that passed (for DMARC alignment)
	if err != nil && len(verificationList) == 0 {
		resultList = append(resultList, &authres.DKIMResult{Value: authres.ResultNone})
	}
	for _, v := range verificationList {
		value := authres.ResultValue(authres.ResultPass)
		if v.Err != nil {
			value = authres.ResultFail
		} else {
			res.DKIMPass = true
			dkimDomainList = append(dkimDomainList, v.Domain)
		}
		resultList = append(resultList, &authres.DKIMResult{
			Value:      value,
			Domain:     v.Domain,
			Identifier: v.Identifier,
		})
	}

	// ── DMARC (RFC 7489) — relaxed alignment ────────────────
	fromDomain := headerFromDomain(raw)
	res.FromParsed = fromDomain != ""
	if fromDomain != "" {
		dmarcValue := authres.ResultValue(authres.ResultNone)
		var lookupOpts *dmarc.LookupOptions
		if opts.LookupTXT != nil {
			lookupOpts = &dmarc.LookupOptions{LookupTXT: opts.LookupTXT}
		}
		record, err := dmarc.LookupWithOptions(fromDomain, lookupOpts)
		if err == nil && record != nil {
			res.DMARCEvaluated = true
			res.DMARCPolicy = string(record.Policy)
			// SPF alignment: envelope from domain vs From header domain
			spfAligned := res.SPFPass && domainAligned(envelopeDomain(opts.EnvelopeFrom), fromDomain)
			// DKIM alignment: passing signature domain vs From header domain
			dkimAligned := false
			for _, d := range dkimDomainList {
				if domainAligned(d, fromDomain) {
					dkimAligned = true
					break
				}
			}
			if spfAligned || dkimAligned {
				dmarcValue = authres.ResultPass
				res.DMARCPass = true
			} else {
				dmarcValue = authres.ResultFail
			}
		}
		resultList = append(resultList, &authres.DMARCResult{
			Value: dmarcValue,
			From:  fromDomain,
		})
	}

	res.Header = []byte("Authentication-Results: " + authres.Format(opts.Hostname, resultList) + "\r\n")
	return res
}

// authresValue converts an SPF result string into an authres value.
func authresValue(s string) authres.ResultValue {
	switch strings.ToLower(s) {
	case "pass":
		return authres.ResultPass
	case "fail":
		return authres.ResultFail
	case "softfail":
		return authres.ResultSoftFail
	case "neutral":
		return authres.ResultNeutral
	case "temperror":
		return authres.ResultTempError
	case "permerror":
		return authres.ResultPermError
	default:
		return authres.ResultNone
	}
}

// envelopeDomain returns the domain of the MAIL FROM address.
func envelopeDomain(addr string) string {
	at := strings.LastIndex(addr, "@")
	if at < 0 {
		return ""
	}
	return strings.ToLower(addr[at+1:])
}

// headerFromDomain extracts the address domain from the From header (the DMARC identity).
func headerFromDomain(raw []byte) string {
	// Scan only the header block (up to the blank line before the body)
	lineList := bytes.Split(raw, []byte("\r\n"))
	var fromLine string
	for i := 0; i < len(lineList); i++ {
		if len(lineList[i]) == 0 {
			break
		}
		if bytes.HasPrefix(bytes.ToLower(lineList[i]), []byte("from:")) {
			fromLine = string(lineList[i][5:])
			// Unfold folded headers
			for j := i + 1; j < len(lineList) && len(lineList[j]) > 0 &&
				(lineList[j][0] == ' ' || lineList[j][0] == '\t'); j++ {
				fromLine += string(lineList[j])
			}
			break
		}
	}
	if fromLine == "" {
		return ""
	}
	// "Name <addr@domain>" or "addr@domain"
	addr := fromLine
	if lt := strings.LastIndex(fromLine, "<"); lt >= 0 {
		if gt := strings.Index(fromLine[lt:], ">"); gt > 0 {
			addr = fromLine[lt+1 : lt+gt]
		}
	}
	return envelopeDomain(strings.TrimSpace(addr))
}

// domainAligned implements DMARC relaxed alignment — aligned if same organizational domain.
// (Simplified implementation: exact match or subdomain relationship. PSL-based
// organizational domain determination will be refined in Phase 4.)
func domainAligned(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if a == b {
		return true
	}
	return strings.HasSuffix(a, "."+b) || strings.HasSuffix(b, "."+a)
}

// FormatSPFError is a logging helper.
func FormatSPFError(result string, err error) string {
	if err != nil {
		return fmt.Sprintf("%s (%v)", result, err)
	}
	return result
}
