// Package auth handles mail authentication (DKIM signing/verification, SPF, DMARC) (Phase 2-4).
//
//   - Outbound: sign with DKIM if the sending domain has a key (called from the queue worker)
//   - Inbound: verify SPF/DKIM/DMARC and attach the Authentication-Results header
//     (Phase 2-4 only records. Policy-based rejection/quarantine comes in Phase 4 anti-spam)
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

// recommendedHeaders are the headers recommended for signing by RFC 6376 §5.4.1.
var recommendedHeaders = []string{
	"From", "To", "Cc", "Subject", "Date", "Message-ID",
	"Reply-To", "In-Reply-To", "References", "MIME-Version",
	"Content-Type", "Content-Transfer-Encoding",
}

// ParsePrivateKey extracts a signing key from PKCS#8 PEM (RSA/Ed25519).
func ParsePrivateKey(pemText string) (crypto.Signer, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, fmt.Errorf("PEM decode failed")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("PKCS#8 parsing: %w", err)
	}
	switch k := key.(type) {
	case *rsa.PrivateKey:
		return k, nil
	case ed25519.PrivateKey:
		return k, nil
	default:
		return nil, fmt.Errorf("unsupported key type %T", key)
	}
}

// SignDKIM attaches a DKIM-Signature header to the raw message and returns it.
// relaxed/relaxed canonicalization — safe against minor whitespace changes in transit.
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
		return nil, fmt.Errorf("DKIM signing: %w", err)
	}
	return out.Bytes(), nil
}
