// Package auth는 메일 인증(DKIM 서명·검증, SPF, DMARC)을 담당한다 (Phase 2-4).
//
//   - 발송: 발신 도메인에 DKIM 키가 있으면 서명 (queue 워커에서 호출)
//   - 수신: SPF/DKIM/DMARC를 검증해 Authentication-Results 헤더를 붙임
//     (Phase 2-4는 기록만. 정책적 거절/격리는 Phase 4 안티스팸에서)
package auth

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	"github.com/emersion/go-msgauth/dkim"
)

// recommendedHeaders는 RFC 6376 §5.4.1 권장 서명 대상 헤더.
var recommendedHeaders = []string{
	"From", "To", "Cc", "Subject", "Date", "Message-ID",
	"Reply-To", "In-Reply-To", "References", "MIME-Version",
	"Content-Type", "Content-Transfer-Encoding",
}

// ParsePrivateKey는 PKCS#8 PEM에서 서명 키를 꺼낸다 (RSA/Ed25519).
func ParsePrivateKey(pemText string) (crypto.Signer, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, fmt.Errorf("PEM 디코드 실패")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("PKCS#8 파싱: %w", err)
	}
	switch k := key.(type) {
	case *rsa.PrivateKey:
		return k, nil
	case ed25519.PrivateKey:
		return k, nil
	default:
		return nil, fmt.Errorf("지원하지 않는 키 타입 %T", key)
	}
}

// SignDKIM은 raw 메시지에 DKIM-Signature 헤더를 붙여 돌려준다.
// relaxed/relaxed canonicalization — 중계 중 사소한 공백 변형에 안전.
func SignDKIM(raw []byte, domain, selector string, signer crypto.Signer) ([]byte, error) {
	opts := &dkim.SignOptions{
		Domain:                 domain,
		Selector:               selector,
		Signer:                 signer,
		HeaderCanonicalization: dkim.CanonicalizationRelaxed,
		BodyCanonicalization:   dkim.CanonicalizationRelaxed,
		HeaderKeys:             recommendedHeaders,
	}
	var out bytes.Buffer
	if err := dkim.Sign(&out, bytes.NewReader(raw), opts); err != nil {
		return nil, fmt.Errorf("DKIM 서명: %w", err)
	}
	return out.Bytes(), nil
}
