package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/emersion/go-msgauth/dkim"
)

// DNS 없이 도는 유닛 테스트 — LookupTXT/SPFResolver를 모의로 주입.

const testMessage = "From: Maro <maro@krisam.in>\r\n" +
	"To: Friend <friend@example.com>\r\n" +
	"Subject: dkim test\r\n" +
	"Date: Wed, 01 Jul 2026 12:00:00 +0900\r\n" +
	"\r\n" +
	"sign me please\r\n"

// genRSAKey는 테스트용 RSA 키를 만들고 (PEM, DNS TXT 값)을 돌려준다.
func genRSAKey(t *testing.T) (pemText, dnsTXT string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("키 생성: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("PKCS#8: %v", err)
	}
	pemText = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("공개키: %v", err)
	}
	dnsTXT = "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(pubDER)
	return
}

// TestDKIMSignAndVerify: 서명 → 모의 DNS로 검증 왕복.
func TestDKIMSignAndVerify(t *testing.T) {
	pemText, dnsTXT := genRSAKey(t)

	signer, err := ParsePrivateKey(pemText)
	if err != nil {
		t.Fatalf("키 파싱: %v", err)
	}
	signed, err := SignDKIM([]byte(testMessage), "krisam.in", "mail", signer)
	if err != nil {
		t.Fatalf("서명: %v", err)
	}
	if !bytes.Contains(signed, []byte("DKIM-Signature:")) {
		t.Fatalf("DKIM-Signature 헤더 없음:\n%.200s", signed)
	}

	// 모의 DNS로 검증
	lookup := func(domain string) ([]string, error) {
		if domain == "mail._domainkey.krisam.in" {
			return []string{dnsTXT}, nil
		}
		return nil, fmt.Errorf("no record for %s", domain)
	}
	verificationList, err := dkim.VerifyWithOptions(bytes.NewReader(signed), &dkim.VerifyOptions{LookupTXT: lookup})
	if err != nil {
		t.Fatalf("검증: %v", err)
	}
	if len(verificationList) != 1 || verificationList[0].Err != nil {
		t.Fatalf("서명 검증 실패: %+v", verificationList)
	}
	if verificationList[0].Domain != "krisam.in" {
		t.Fatalf("서명 도메인 이상: %s", verificationList[0].Domain)
	}
	t.Log("✔ DKIM 서명 → 검증 왕복 (RSA-2048, relaxed/relaxed)")
}

// TestDKIMEd25519: Ed25519 키도 동작.
func TestDKIMEd25519(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("키 생성: %v", err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	signer, err := ParsePrivateKey(pemText)
	if err != nil {
		t.Fatalf("키 파싱: %v", err)
	}
	signed, err := SignDKIM([]byte(testMessage), "krisam.in", "ed", signer)
	if err != nil {
		t.Fatalf("서명: %v", err)
	}

	dnsTXT := "v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(pub)
	lookup := func(domain string) ([]string, error) {
		return []string{dnsTXT}, nil
	}
	verificationList, err := dkim.VerifyWithOptions(bytes.NewReader(signed), &dkim.VerifyOptions{LookupTXT: lookup})
	if err != nil || len(verificationList) != 1 || verificationList[0].Err != nil {
		t.Fatalf("Ed25519 검증 실패: %v %+v", err, verificationList)
	}
	t.Log("✔ Ed25519 DKIM 서명/검증")
}

// TestVerifyInbound: SPF pass + DKIM pass + DMARC pass 시나리오 (모의 DNS).
func TestVerifyInbound(t *testing.T) {
	pemText, dnsTXT := genRSAKey(t)
	signer, _ := ParsePrivateKey(pemText)
	signed, err := SignDKIM([]byte(testMessage), "krisam.in", "mail", signer)
	if err != nil {
		t.Fatalf("서명: %v", err)
	}

	lookup := func(domain string) ([]string, error) {
		switch domain {
		case "mail._domainkey.krisam.in":
			return []string{dnsTXT}, nil
		case "_dmarc.krisam.in":
			return []string{"v=DMARC1; p=reject"}, nil
		}
		return nil, fmt.Errorf("no record for %s", domain)
	}

	// SPF는 실 DNS를 타므로 여기선 결과만 관찰 (fail이어도 DKIM 정렬로 DMARC pass)
	vr := VerifyInbound(signed, VerifyOptions{
		RemoteIP:     net.ParseIP("192.0.2.1"),
		HeloName:     "sender.test",
		EnvelopeFrom: "maro@krisam.in",
		Hostname:     "mx.krisam.in",
		LookupTXT:    lookup,
	})

	header := string(vr.Header)
	if !strings.HasPrefix(header, "Authentication-Results: mx.krisam.in;") {
		t.Fatalf("헤더 형식 이상: %q", header)
	}
	if !vr.DKIMPass {
		t.Fatalf("DKIM pass여야: %s", header)
	}
	if !vr.DMARCPass {
		t.Fatalf("DMARC pass여야 (DKIM 정렬): %s", header)
	}
	if !strings.Contains(header, "dkim=pass") || !strings.Contains(header, "dmarc=pass") {
		t.Fatalf("헤더 값 이상: %s", header)
	}
	t.Logf("✔ 수신 검증: %s", strings.TrimSpace(header))
}

// TestVerifyInboundDKIMFail: 본문 변조 시 DKIM fail + DMARC fail.
func TestVerifyInboundDKIMFail(t *testing.T) {
	pemText, dnsTXT := genRSAKey(t)
	signer, _ := ParsePrivateKey(pemText)
	signed, _ := SignDKIM([]byte(testMessage), "krisam.in", "mail", signer)

	// 본문 변조
	tampered := bytes.Replace(signed, []byte("sign me please"), []byte("tampered body!!"), 1)

	lookup := func(domain string) ([]string, error) {
		switch domain {
		case "mail._domainkey.krisam.in":
			return []string{dnsTXT}, nil
		case "_dmarc.krisam.in":
			return []string{"v=DMARC1; p=reject"}, nil
		}
		return nil, fmt.Errorf("no record")
	}
	vr := VerifyInbound(tampered, VerifyOptions{
		RemoteIP:     net.ParseIP("192.0.2.1"),
		HeloName:     "sender.test",
		EnvelopeFrom: "maro@krisam.in",
		Hostname:     "mx.krisam.in",
		LookupTXT:    lookup,
	})
	if vr.DKIMPass {
		t.Fatal("변조됐는데 DKIM pass")
	}
	if !strings.Contains(string(vr.Header), "dkim=fail") {
		t.Fatalf("dkim=fail이어야: %s", vr.Header)
	}
	t.Logf("✔ 변조 감지: %s", strings.TrimSpace(string(vr.Header)))
}

// TestHeaderFromDomain: From 헤더 도메인 추출.
func TestHeaderFromDomain(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"From: Maro <maro@krisam.in>\r\n\r\nbody", "krisam.in"},
		{"From: maro@krisam.in\r\n\r\nbody", "krisam.in"},
		{"Subject: x\r\nFrom: \"M, aro\" <a@B.COM>\r\n\r\nbody", "b.com"},
		{"Subject: x\r\n\r\nFrom: not-a-header@nope.com\r\n", ""},
	}
	for _, c := range cases {
		if got := headerFromDomain([]byte(c.raw)); got != c.want {
			t.Fatalf("headerFromDomain(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
	t.Log("✔ From 도메인 추출 (본문의 From: 오탐 없음)")
}
