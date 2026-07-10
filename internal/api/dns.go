package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// DNS 검증 (0005) — 도메인의 메일 관련 DNS 레코드를 실조회해서
// 어드민 화면에 ✅/⚠️/❌ 배지를 띄운다. 조회 실패는 missing으로 취급.
//
// 항목: MX(우리 서버), SPF(TXT v=spf1), DKIM(<selector>._domainkey — DB 키와
// 일치 여부까지), DMARC(_dmarc TXT).

type dnsCheckDTO struct {
	// Status: "ok" | "warn" | "missing"
	Status string `json:"status"`
	// Found는 DNS에서 실제로 찾은 값 (없으면 빈 문자열).
	Found string `json:"found"`
	// Expected는 권장/기대 값 — 어드민이 복붙할 수 있게.
	Expected string `json:"expected,omitempty"`
	// Note는 상태 설명.
	Note string `json:"note,omitempty"`
}

type dnsVerifyDTO struct {
	Domain string      `json:"domain"`
	MX     dnsCheckDTO `json:"mx"`
	SPF    dnsCheckDTO `json:"spf"`
	DKIM   dnsCheckDTO `json:"dkim"`
	DMARC  dnsCheckDTO `json:"dmarc"`
}

// handleVerifyDomainDNS는 도메인의 DNS 상태를 실조회한다.
func (s *Server) handleVerifyDomainDNS(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	// 도메인 로드 (DKIM 키 포함 필요 — ListDomain에서 찾기)
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
	writeJSON(w, http.StatusOK, out)
}

// resolver는 공용 DNS(1.1.1.1)로 직접 질의한다.
// 로컬 리졸버는 스플릿 DNS(내부 도메인 오버라이드)일 수 있는데,
// 이 검증의 목적은 "바깥 세상이 보는 DNS"라서 공용 경유가 맞다.
var resolver = &net.Resolver{
	PreferGo: true,
	Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		d := net.Dialer{Timeout: 5 * time.Second}
		return d.DialContext(ctx, network, "1.1.1.1:53")
	},
}

func lookupTXTJoined(ctx context.Context, name string) []string {
	txtList, err := resolver.LookupTXT(ctx, name)
	if err != nil {
		return nil
	}
	return txtList
}

func checkMX(ctx context.Context, domain, expectedHost string) dnsCheckDTO {
	mxList, err := resolver.LookupMX(ctx, domain)
	if err != nil || len(mxList) == 0 {
		return dnsCheckDTO{
			Status: "missing", Expected: expectedHost,
			Note: "MX 레코드 없음 — 수신하려면 이 서버를 가리키는 MX가 필요",
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
		Note: "MX가 이 서버(" + expectedHost + ")를 가리키지 않음",
	}
}

func checkSPF(ctx context.Context, domain string) dnsCheckDTO {
	for _, txt := range lookupTXTJoined(ctx, domain) {
		if strings.HasPrefix(strings.ToLower(txt), "v=spf1") {
			return dnsCheckDTO{Status: "ok", Found: txt}
		}
	}
	return dnsCheckDTO{
		Status: "missing", Expected: "v=spf1 ... ~all",
		Note: "SPF 없음 — relay 프로바이더가 주는 include를 등록",
	}
}

func checkDKIM(ctx context.Context, domain, selector, privateKeyPEM string) dnsCheckDTO {
	if selector == "" {
		return dnsCheckDTO{Status: "warn", Note: "DKIM 키 미생성 — 도메인 관리에서 생성"}
	}
	name := selector + "._domainkey." + domain
	txtList := lookupTXTJoined(ctx, name)
	if len(txtList) == 0 {
		expected := ""
		if txt, err := dkimPublicTXT(privateKeyPEM); err == nil {
			expected = txt
		}
		return dnsCheckDTO{
			Status: "missing", Expected: expected,
			Note: name + " TXT 없음",
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
			Note: "DNS의 공개키가 서버 키와 다름 — 서명 검증 실패함",
		}
	}
	return dnsCheckDTO{Status: "ok", Found: found, Note: "존재 확인 (키 비교 불가)"}
}

// extractDKIMKey는 TXT에서 p= 값만 뽑는다 (공백/따옴표 분할 무시 비교용).
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
	for _, txt := range lookupTXTJoined(ctx, name) {
		if strings.HasPrefix(strings.ToLower(txt), "v=dmarc1") {
			return dnsCheckDTO{Status: "ok", Found: txt}
		}
	}
	return dnsCheckDTO{
		Status:   "missing",
		Expected: "v=DMARC1; p=none; rua=mailto:postmaster@" + domain,
		Note:     name + " TXT 없음",
	}
}
