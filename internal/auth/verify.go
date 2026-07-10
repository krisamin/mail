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

// VerifyOptions는 수신 검증에 필요한 입력.
type VerifyOptions struct {
	// RemoteIP는 SMTP 클라이언트의 IP (SPF 대상).
	RemoteIP net.IP
	// HeloName은 EHLO/HELO 이름.
	HeloName string
	// EnvelopeFrom은 MAIL FROM 주소.
	EnvelopeFrom string
	// Hostname은 Authentication-Results의 authserv-id.
	Hostname string

	// LookupTXT는 테스트용 DNS 오버라이드 (nil이면 실 DNS).
	LookupTXT func(domain string) ([]string, error)
	// SPFResolver는 테스트용 (nil이면 실 DNS).
	SPFResolver spf.DNSResolver
}

// VerifyResult는 수신 검증 결과 요약.
type VerifyResult struct {
	// Header는 메시지 앞에 붙일 Authentication-Results 헤더 (CRLF 포함).
	Header []byte
	// SPFPass / DKIMPass / DMARCPass — 정책 판단용 요약.
	SPFPass   bool
	DKIMPass  bool
	DMARCPass bool
	// DMARCEvaluated는 발신 도메인에 DMARC 레코드가 있어 판정이 이뤄졌는지.
	DMARCEvaluated bool
	// DMARCPolicy는 발신 도메인이 공표한 정책 ("none"|"quarantine"|"reject").
	// DMARCEvaluated가 true일 때만 의미 있다.
	DMARCPolicy string
}

// VerifyInbound는 수신 메일의 SPF/DKIM/DMARC를 검증하고
// Authentication-Results 헤더를 만든다. 검증 실패해도 에러가 아니라
// 결과에 기록만 한다 (거절 정책은 Phase 4).
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
	var dkimDomainList []string // pass한 서명 도메인 (DMARC 정렬용)
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
			// SPF 정렬: envelope from 도메인 vs From 헤더 도메인
			spfAligned := res.SPFPass && domainAligned(envelopeDomain(opts.EnvelopeFrom), fromDomain)
			// DKIM 정렬: pass한 서명 도메인 vs From 헤더 도메인
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

// authresValue는 SPF 결과 문자열을 authres 값으로 변환한다.
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

// envelopeDomain은 MAIL FROM 주소의 도메인.
func envelopeDomain(addr string) string {
	at := strings.LastIndex(addr, "@")
	if at < 0 {
		return ""
	}
	return strings.ToLower(addr[at+1:])
}

// headerFromDomain은 From 헤더의 주소 도메인을 뽑는다 (DMARC 기준 신원).
func headerFromDomain(raw []byte) string {
	// 헤더 블록만 스캔 (본문 앞 빈 줄까지)
	lineList := bytes.Split(raw, []byte("\r\n"))
	var fromLine string
	for i := 0; i < len(lineList); i++ {
		if len(lineList[i]) == 0 {
			break
		}
		if bytes.HasPrefix(bytes.ToLower(lineList[i]), []byte("from:")) {
			fromLine = string(lineList[i][5:])
			// folded header 이어붙이기
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
	// "Name <addr@domain>" 또는 "addr@domain"
	addr := fromLine
	if lt := strings.LastIndex(fromLine, "<"); lt >= 0 {
		if gt := strings.Index(fromLine[lt:], ">"); gt > 0 {
			addr = fromLine[lt+1 : lt+gt]
		}
	}
	return envelopeDomain(strings.TrimSpace(addr))
}

// domainAligned는 DMARC relaxed alignment — 같은 조직 도메인이면 정렬.
// (간이 구현: 완전 일치 또는 서브도메인 관계. PSL 기반 조직 도메인
// 판정은 Phase 4에서 정교화.)
func domainAligned(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if a == b {
		return true
	}
	return strings.HasSuffix(a, "."+b) || strings.HasSuffix(b, "."+a)
}

// FormatSPFError는 로그용 헬퍼.
func FormatSPFError(result string, err error) string {
	if err != nil {
		return fmt.Sprintf("%s (%v)", result, err)
	}
	return result
}
