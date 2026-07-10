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

// Unit tests that run without DNS — LookupTXT/SPFResolver are injected as mocks.

const testMessage = "From: Maro <maro@krisam.in>\r\n" +
	"To: Friend <friend@example.com>\r\n" +
	"Subject: dkim test\r\n" +
	"Date: Wed, 01 Jul 2026 12:00:00 +0900\r\n" +
	"\r\n" +
	"sign me please\r\n"

// genRSAKey creates an RSA key for testing and returns (PEM, DNS TXT value).
func genRSAKey(t *testing.T) (pemText, dnsTXT string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("key generation: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("PKCS#8: %v", err)
	}
	pemText = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	dnsTXT = "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(pubDER)
	return
}

// TestDKIMSignAndVerify: sign → verify round-trip with mock DNS.
func TestDKIMSignAndVerify(t *testing.T) {
	pemText, dnsTXT := genRSAKey(t)

	signer, err := ParsePrivateKey(pemText)
	if err != nil {
		t.Fatalf("key parsing: %v", err)
	}
	signed, err := SignDKIM([]byte(testMessage), "krisam.in", "mail", signer)
	if err != nil {
		t.Fatalf("signing: %v", err)
	}
	if !bytes.Contains(signed, []byte("DKIM-Signature:")) {
		t.Fatalf("missing DKIM-Signature header:\n%.200s", signed)
	}

	// Verify with mock DNS
	lookup := func(domain string) ([]string, error) {
		if domain == "mail._domainkey.krisam.in" {
			return []string{dnsTXT}, nil
		}
		return nil, fmt.Errorf("no record for %s", domain)
	}
	verificationList, err := dkim.VerifyWithOptions(bytes.NewReader(signed), &dkim.VerifyOptions{LookupTXT: lookup})
	if err != nil {
		t.Fatalf("verification: %v", err)
	}
	if len(verificationList) != 1 || verificationList[0].Err != nil {
		t.Fatalf("signature verification failed: %+v", verificationList)
	}
	if verificationList[0].Domain != "krisam.in" {
		t.Fatalf("unexpected signature domain: %s", verificationList[0].Domain)
	}
	t.Log("✔ DKIM sign → verify round-trip (RSA-2048, relaxed/relaxed)")
}

// TestDKIMEd25519: Ed25519 keys work too.
func TestDKIMEd25519(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key generation: %v", err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	signer, err := ParsePrivateKey(pemText)
	if err != nil {
		t.Fatalf("key parsing: %v", err)
	}
	signed, err := SignDKIM([]byte(testMessage), "krisam.in", "ed", signer)
	if err != nil {
		t.Fatalf("signing: %v", err)
	}

	dnsTXT := "v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(pub)
	lookup := func(domain string) ([]string, error) {
		return []string{dnsTXT}, nil
	}
	verificationList, err := dkim.VerifyWithOptions(bytes.NewReader(signed), &dkim.VerifyOptions{LookupTXT: lookup})
	if err != nil || len(verificationList) != 1 || verificationList[0].Err != nil {
		t.Fatalf("Ed25519 verification failed: %v %+v", err, verificationList)
	}
	t.Log("✔ Ed25519 DKIM sign/verify")
}

// TestVerifyInbound: SPF pass + DKIM pass + DMARC pass scenario (mock DNS).
func TestVerifyInbound(t *testing.T) {
	pemText, dnsTXT := genRSAKey(t)
	signer, _ := ParsePrivateKey(pemText)
	signed, err := SignDKIM([]byte(testMessage), "krisam.in", "mail", signer)
	if err != nil {
		t.Fatalf("signing: %v", err)
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

	// SPF goes through real DNS, so here we only observe the result (even if it fails, DKIM alignment yields DMARC pass)
	vr := VerifyInbound(signed, VerifyOptions{
		RemoteIP:     net.ParseIP("192.0.2.1"),
		HeloName:     "sender.test",
		EnvelopeFrom: "maro@krisam.in",
		Hostname:     "mx.krisam.in",
		LookupTXT:    lookup,
	})

	header := string(vr.Header)
	if !strings.HasPrefix(header, "Authentication-Results: mx.krisam.in;") {
		t.Fatalf("unexpected header format: %q", header)
	}
	if !vr.DKIMPass {
		t.Fatalf("DKIM should pass: %s", header)
	}
	if !vr.DMARCPass {
		t.Fatalf("DMARC should pass (DKIM alignment): %s", header)
	}
	if !strings.Contains(header, "dkim=pass") || !strings.Contains(header, "dmarc=pass") {
		t.Fatalf("unexpected header values: %s", header)
	}
	t.Logf("✔ inbound verification: %s", strings.TrimSpace(header))
}

// TestVerifyInboundDKIMFail: tampered body causes DKIM fail + DMARC fail.
func TestVerifyInboundDKIMFail(t *testing.T) {
	pemText, dnsTXT := genRSAKey(t)
	signer, _ := ParsePrivateKey(pemText)
	signed, _ := SignDKIM([]byte(testMessage), "krisam.in", "mail", signer)

	// Tamper with the body
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
		t.Fatal("tampered but DKIM passed")
	}
	if !strings.Contains(string(vr.Header), "dkim=fail") {
		t.Fatalf("should be dkim=fail: %s", vr.Header)
	}
	// Fields used for policy enforcement decisions — the p=reject record must have been read
	if !vr.DMARCEvaluated || vr.DMARCPolicy != "reject" {
		t.Fatalf("failed to read DMARC policy: evaluated=%v policy=%q", vr.DMARCEvaluated, vr.DMARCPolicy)
	}
	if vr.DMARCPass {
		t.Fatal("tampered mail must not pass DMARC")
	}
	t.Logf("✔ tamper detection + policy read (p=%s): %s", vr.DMARCPolicy, strings.TrimSpace(string(vr.Header)))
}

// TestVerifyInboundQuarantinePolicy: reads p=quarantine.
func TestVerifyInboundQuarantinePolicy(t *testing.T) {
	pemText, dnsTXT := genRSAKey(t)
	signer, _ := ParsePrivateKey(pemText)
	signed, _ := SignDKIM([]byte(testMessage), "krisam.in", "mail", signer)
	tampered := bytes.Replace(signed, []byte("sign me please"), []byte("tampered body!!"), 1)

	lookup := func(domain string) ([]string, error) {
		switch domain {
		case "mail._domainkey.krisam.in":
			return []string{dnsTXT}, nil
		case "_dmarc.krisam.in":
			return []string{"v=DMARC1; p=quarantine"}, nil
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
	if !vr.DMARCEvaluated || vr.DMARCPolicy != "quarantine" || vr.DMARCPass {
		t.Fatalf("failed to read quarantine policy: evaluated=%v policy=%q pass=%v",
			vr.DMARCEvaluated, vr.DMARCPolicy, vr.DMARCPass)
	}
	t.Log("✔ p=quarantine read (→ deliver to Junk)")
}

// TestVerifyInboundNoDMARCRecord: no record means no enforcement.
func TestVerifyInboundNoDMARCRecord(t *testing.T) {
	vr := VerifyInbound([]byte(testMessage), VerifyOptions{
		RemoteIP:     net.ParseIP("192.0.2.1"),
		HeloName:     "sender.test",
		EnvelopeFrom: "maro@krisam.in",
		Hostname:     "mx.krisam.in",
		LookupTXT:    func(string) ([]string, error) { return nil, fmt.Errorf("no record") },
	})
	if vr.DMARCEvaluated {
		t.Fatal("no record but evaluated=true")
	}
	t.Log("✔ no DMARC record → not subject to enforcement")
}

// TestHeaderFromDomain: From header domain extraction.
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
	t.Log("✔ From domain extraction (no false positives on From: in the body)")
}
