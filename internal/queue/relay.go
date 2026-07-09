package queue

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"

	"github.com/krisamin/mail/internal/store"
)

// RelayConfig는 SMTP relay(SES/Postmark/기타 submission 서버) 접속 정보.
// DD-04: 직접 MX 발송 대신 relay 경유가 기본.
type RelayConfig struct {
	Addr     string // 예: email-smtp.ap-northeast-2.amazonaws.com:587
	Username string
	Password string
	// StartTLS가 true면 STARTTLS 후 AUTH (587 표준). false면 평문 (테스트용).
	StartTLS bool
}

// RelaySender는 SMTP relay로 발송하는 Sender 구현.
type RelaySender struct {
	cfg RelayConfig
}

// NewRelaySender는 relay 발송기를 만든다.
func NewRelaySender(cfg RelayConfig) *RelaySender {
	return &RelaySender{cfg: cfg}
}

var _ Sender = (*RelaySender)(nil)

// Send는 relay에 접속해 한 통을 발송한다.
// 5xx 응답은 PermanentError로 감싸 재시도를 막는다.
func (r *RelaySender) Send(ctx context.Context, from, rcpt string, raw []byte) error {
	var c *gosmtp.Client
	var err error
	if r.cfg.StartTLS {
		c, err = gosmtp.DialStartTLS(r.cfg.Addr, &tls.Config{ServerName: hostOf(r.cfg.Addr)})
	} else {
		c, err = gosmtp.Dial(r.cfg.Addr)
	}
	if err != nil {
		return fmt.Errorf("relay 접속: %w", err) // 접속 실패 = 일시 오류
	}
	defer c.Close()

	if r.cfg.Username != "" {
		auth := sasl.NewPlainClient("", r.cfg.Username, r.cfg.Password)
		if err := c.Auth(auth); err != nil {
			return wrapSMTPErr(fmt.Errorf("relay AUTH: %w", err), err)
		}
	}
	if err := c.Mail(from, nil); err != nil {
		return wrapSMTPErr(fmt.Errorf("MAIL: %w", err), err)
	}
	if err := c.Rcpt(rcpt, nil); err != nil {
		return wrapSMTPErr(fmt.Errorf("RCPT: %w", err), err)
	}
	w, err := c.Data()
	if err != nil {
		return wrapSMTPErr(fmt.Errorf("DATA: %w", err), err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("본문 전송: %w", err)
	}
	if err := w.Close(); err != nil {
		return wrapSMTPErr(fmt.Errorf("본문 완료: %w", err), err)
	}
	return c.Quit()
}

// wrapSMTPErr는 5xx SMTP 오류를 PermanentError로 승격한다.
func wrapSMTPErr(wrapped, original error) error {
	if smtpErr, ok := original.(*gosmtp.SMTPError); ok && smtpErr.Code >= 500 {
		return &PermanentError{Err: wrapped}
	}
	return wrapped
}

func hostOf(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}

// ── DB 해석 Sender (0005) ───────────────────────────────────

// ResolvingSender는 발송 시점에 발신 도메인의 relay를 DB에서 해석한다.
// 도메인 지정 relay → default relay → 오류(재시도).
// relay를 어드민에서 바꾸면 재기동 없이 다음 발송부터 반영된다.
type ResolvingSender struct {
	store store.Store
}

// NewResolvingSender는 DB 해석 발송기를 만든다.
func NewResolvingSender(st store.Store) *ResolvingSender {
	return &ResolvingSender{store: st}
}

var _ Sender = (*ResolvingSender)(nil)

// Send는 relay를 해석한 뒤 RelaySender로 위임한다.
func (r *ResolvingSender) Send(ctx context.Context, from, rcpt string, raw []byte) error {
	senderDomain := ""
	if i := strings.LastIndex(from, "@"); i >= 0 {
		senderDomain = from[i+1:]
	}
	rl, err := r.store.ResolveRelay(ctx, senderDomain)
	if err == nil {
		cfg := RelayConfig{
			Addr:     fmt.Sprintf("%s:%d", rl.Host, rl.Port),
			Username: rl.Username,
			Password: rl.Password,
			StartTLS: rl.StartTLS,
		}
		return NewRelaySender(cfg).Send(ctx, from, rcpt, raw)
	}
	if errors.Is(err, store.ErrNotFound) {
		// relay가 없음 — 어드민이 곧 추가할 수 있으니 일시 오류로 재시도.
		return fmt.Errorf("도메인 %q의 relay 미설정 (어드민에서 relay 추가 필요)", senderDomain)
	}
	return fmt.Errorf("relay 해석: %w", err)
}
