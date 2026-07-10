package postgres

import (
	"bytes"

	"github.com/emersion/go-message/mail"
)

// parseHeaderCache extracts Subject/From from the raw mail (for the header cache).
// best-effort — returns empty strings on parse failure.
func parseHeaderCache(raw []byte) (subject, fromAddr string) {
	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return "", ""
	}
	h := mr.Header
	subject, _ = h.Subject()
	if addrs, err := h.AddressList("From"); err == nil && len(addrs) > 0 {
		fromAddr = addrs[0].Address
	}
	return subject, fromAddr
}
