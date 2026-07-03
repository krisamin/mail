package queue

import (
	"context"
	"crypto/tls"
	"fmt"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"
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
