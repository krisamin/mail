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

// TestDKIMSignerFromStore: 도메인에 키가 있으면 서명, 없으면 통과.
func TestDKIMSignerFromStore(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	seedUser(t, st, "maro@krisam.in", "dkim-pw")

	// 키 생성 + 도메인에 저장
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("키 생성: %v", err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if _, err := st.Pool().Exec(ctx,
		`UPDATE domains SET dkim_selector = 'mail', dkim_private_key = $1 WHERE name = 'krisam.in'`,
		pemText); err != nil {
		t.Fatalf("키 저장: %v", err)
	}

	signer := NewDKIMSigner(st)
	raw := []byte("From: maro@krisam.in\r\nTo: x@example.com\r\nSubject: s\r\n\r\nbody\r\n")

	// 1) 키 있는 도메인 → DKIM-Signature 붙음
	signed, err := signer(ctx, "maro@krisam.in", raw)
	if err != nil {
		t.Fatalf("서명: %v", err)
	}
	if !bytes.Contains(signed, []byte("DKIM-Signature:")) {
		t.Fatalf("서명 헤더 없음:\n%.200s", signed)
	}

	// 2) 키 없는(우리 소관 아닌) 도메인 → 원문 통과
	passed, err := signer(ctx, "someone@unknown.example", raw)
	if err != nil {
		t.Fatalf("통과 경로: %v", err)
	}
	if !bytes.Equal(passed, raw) {
		t.Fatal("키 없는 도메인은 원문 그대로여야")
	}

	// 3) selector를 지우면 → 통과
	_, _ = st.Pool().Exec(ctx, `UPDATE domains SET dkim_selector = NULL WHERE name = 'krisam.in'`)
	passed2, err := signer(ctx, "maro@krisam.in", raw)
	if err != nil || !bytes.Equal(passed2, raw) {
		t.Fatalf("selector 없으면 통과여야: %v", err)
	}
	t.Log("✔ DKIM 서명 훅: 키 있으면 서명 / 없으면 통과")
}
