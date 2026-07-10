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

// DNS 검증 (0005) — 도메인의 메일 관련 DNS 레코드를 실조회해서
// 어드민 화면에 ✅/⚠️/❌ 배지를 띄운다. 조회 실패(NXDOMAIN 아님)는
// missing이 아니라 error 상태로 구분한다.
//
// 항목: MX(우리 서버), SPF(TXT v=spf1), DKIM(<selector>._domainkey — DB 키와
// 일치 여부까지), DMARC(_dmarc TXT).
// 자동감지: SRV(_imaps/_submissions/_submission — RFC 6186/8314),
// autoconfig(Thunderbird XML 자동설정 호스트).

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
	// 자동감지 — 클라이언트가 이메일 주소만으로 서버를 찾게 하는 레코드.
	// 필드명의 imaps/submissions는 복수형이 아니라 DNS 서비스 라벨 그대로다
	// (_imaps._tcp = IMAP over TLS 993, _submissions._tcp = implicit TLS 465,
	//  _submission._tcp = STARTTLS 587).
	SRVImaps       dnsCheckDTO `json:"srvImaps"`
	SRVSubmissions dnsCheckDTO `json:"srvSubmissions"`
	SRVSubmission  dnsCheckDTO `json:"srvSubmission"`
	Autoconfig     dnsCheckDTO `json:"autoconfig"`
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
	out.SRVImaps = checkSRV(ctx, "imaps", name, s.hostname, 993)
	out.SRVSubmissions = checkSRV(ctx, "submissions", name, s.hostname, 465)
	out.SRVSubmission = checkSRV(ctx, "submission", name, s.hostname, 587)
	out.Autoconfig = checkAutoconfigDNS(ctx, name, s.hostname)
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

// dnsUnavailable은 "레코드 없음(NXDOMAIN/NODATA)"이 아닌 조회 실패 판정.
// 일시적 DNS 장애를 missing으로 오판하면 어드민이 멀쩡한 레코드를
// 재설정하러 가는 사고가 난다 — error 상태로 구분해서 표기.
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

// dnsErrorCheck는 조회 실패용 공통 DTO.
func dnsErrorCheck(name string) dnsCheckDTO {
	return dnsCheckDTO{
		Status: "error",
		Note:   name + " 조회 실패 — DNS 응답 없음 (레코드 없음과 다름, 잠시 후 재시도)",
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
		Note: "SPF 없음 — relay 프로바이더가 주는 include를 등록",
	}
}

func checkDKIM(ctx context.Context, domain, selector, privateKeyPEM string) dnsCheckDTO {
	if selector == "" {
		return dnsCheckDTO{Status: "warn", Note: "DKIM 키 미생성 — 도메인 관리에서 생성"}
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
		Note:     name + " TXT 없음",
	}
}

// checkSRV는 RFC 6186/8314 클라이언트 자동감지 SRV 레코드를 검사한다.
// service: "imaps"(993) | "submissions"(465) | "submission"(587).
// 메일 클라이언트는 이메일 도메인의 이 레코드로 접속할 서버를 찾는다 —
// 없으면 imap.<domain> 같은 추측에 의존해 엉뚱한 곳에 붙을 수 있다.
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
			Note: recordName + " SRV 없음 — 클라이언트 자동설정이 서버를 못 찾음",
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
		Note: fmt.Sprintf("SRV가 이 서버(%s:%d)를 가리키지 않음", expectedHost, expectedPort),
	}
}

// checkAutoconfigDNS는 Thunderbird 계열 자동설정 호스트를 검사한다.
// 클라이언트는 http(s)://autoconfig.<domain>/mail/config-v1.1.xml 을 먼저
// 찾으므로, autoconfig.<domain>이 이 서버(웹)로 향해야 XML을 서빙할 수 있다.
// A/AAAA/CNAME 무엇이든 최종적으로 우리 서버 호스트로 해석되면 ok.
func checkAutoconfigDNS(ctx context.Context, domain, expectedHost string) dnsCheckDTO {
	recordName := "autoconfig." + domain
	expected := recordName + " IN CNAME " + expectedHost + "."
	if expectedHost == "" {
		expected = ""
	}

	// CNAME이 기대 호스트로 바로 향하는지 먼저 확인 (가장 명확한 신호).
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
			Note: recordName + " 없음 — Thunderbird 자동설정 불가",
		}
	}
	found := recordName + " → " + strings.Join(addrList, ", ")
	// IP 비교: 기대 호스트의 IP 집합과 겹치면 ok (CNAME 없이 A로 걸어도 인정).
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
			Note: "autoconfig가 이 서버(" + expectedHost + ")로 해석되지 않음",
		}
	}
	return dnsCheckDTO{Status: "ok", Found: found}
}
