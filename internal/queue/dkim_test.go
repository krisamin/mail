package queue

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
)

// TestDKIMSignerFromStore: signs when the domain has a key, passes through when not.
func TestDKIMSignerFromStore(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	seedAccount(t, st, "maro@krisam.in", "dkim-pw")

	// generate a key + store it on the domain
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("key generation: %v", err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if _, err := st.Pool().Exec(ctx,
		`UPDATE domain SET dkim_selector = 'mail', dkim_private_key = $1 WHERE name = 'krisam.in'`,
		pemText); err != nil {
		t.Fatalf("key save: %v", err)
	}

	signer := NewDKIMSigner(st)
	raw := []byte("From: maro@krisam.in\r\nTo: x@example.com\r\nSubject: s\r\n\r\nbody\r\n")

	// 1) domain with a key → DKIM-Signature attached
	signed, err := signer(ctx, "maro@krisam.in", raw)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !bytes.Contains(signed, []byte("DKIM-Signature:")) {
		t.Fatalf("missing signature header:\n%.200s", signed)
	}

	// 2) domain without a key (not ours) → raw passes through
	passed, err := signer(ctx, "someone@unknown.example", raw)
	if err != nil {
		t.Fatalf("pass-through path: %v", err)
	}
	if !bytes.Equal(passed, raw) {
		t.Fatal("a domain without a key must pass the raw message through unchanged")
	}

	// 3) clearing the selector → pass through
	_, _ = st.Pool().Exec(ctx, `UPDATE domain SET dkim_selector = NULL WHERE name = 'krisam.in'`)
	passed2, err := signer(ctx, "maro@krisam.in", raw)
	if err != nil || !bytes.Equal(passed2, raw) {
		t.Fatalf("should pass through without a selector: %v", err)
	}
	t.Log("✔ DKIM signing hook: signs with a key / passes through without")
}
