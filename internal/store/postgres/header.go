package postgres

import (
	"bytes"

	"github.com/emersion/go-message/mail"
)

// parseHeaderCache는 raw 메일에서 Subject/From을 뽑는다 (헤더 캐시용).
// best-effort — 파싱 실패 시 빈 문자열 반환.
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
