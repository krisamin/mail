package queue

import (
	"context"
	"errors"
	"strings"

	"github.com/krisamin/mail/internal/auth"
	"github.com/krisamin/mail/internal/store"
)

// NewDKIMSigner는 store에서 발신 도메인의 DKIM 키를 찾아 서명하는 SignFunc를 만든다.
// 도메인에 키가 없으면(selector 비어있음) 원문 그대로 통과.
func NewDKIMSigner(st store.Store) SignFunc {
	return func(ctx context.Context, envelopeFrom string, raw []byte) ([]byte, error) {
		at := strings.LastIndex(envelopeFrom, "@")
		if at < 0 {
			return raw, nil
		}
		domainName := strings.ToLower(envelopeFrom[at+1:])

		d, err := st.FindDomain(ctx, domainName)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return raw, nil // 우리 도메인 아님 — 서명 불가, 통과
			}
			return nil, err
		}
		if d.DKIMSelector == "" || d.DKIMPrivateKey == "" {
			return raw, nil // 키 미설정 — 통과
		}

		signer, err := auth.ParsePrivateKey(d.DKIMPrivateKey)
		if err != nil {
			return nil, err
		}
		return auth.SignDKIM(raw, domainName, d.DKIMSelector, signer)
	}
}
