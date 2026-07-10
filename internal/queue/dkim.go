package queue

import (
	"context"
	"errors"
	"strings"

	"github.com/krisamin/mail/internal/auth"
	"github.com/krisamin/mail/internal/store"
)

// NewDKIMSigner builds a SignFunc that looks up the sender domain's DKIM
// key in the store and signs. If the domain has no key (empty selector),
// the raw message passes through as-is.
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
				return raw, nil // not our domain — can't sign, pass through
			}
			return nil, err
		}
		if d.DKIMSelector == "" || d.DKIMPrivateKey == "" {
			return raw, nil // no key configured — pass through
		}

		signer, err := auth.ParsePrivateKey(d.DKIMPrivateKey)
		if err != nil {
			return nil, err
		}
		return auth.SignDKIM(raw, domainName, d.DKIMSelector, signer)
	}
}
