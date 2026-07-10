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

// RelayConfig holds connection info for an SMTP relay (SES/Postmark/other
// submission servers). DD-04: relay is the default instead of direct MX sending.
type RelayConfig struct {
	Addr     string // e.g. email-smtp.ap-northeast-2.amazonaws.com:587
	Username string
	Password string
	// StartTLS=true does STARTTLS then AUTH (587 standard). false is plaintext (for tests).
	StartTLS bool
}

// RelaySender is a Sender implementation that sends via an SMTP relay.
type RelaySender struct {
	cfg RelayConfig
}

// NewRelaySender creates a relay sender.
func NewRelaySender(cfg RelayConfig) *RelaySender {
	return &RelaySender{cfg: cfg}
}

var _ Sender = (*RelaySender)(nil)

// Send connects to the relay and sends one message.
// 5xx responses are wrapped in PermanentError to prevent retries.
func (r *RelaySender) Send(ctx context.Context, from, rcpt string, raw []byte) error {
	var c *gosmtp.Client
	var err error
	if r.cfg.StartTLS {
		c, err = gosmtp.DialStartTLS(r.cfg.Addr, &tls.Config{ServerName: hostOf(r.cfg.Addr)})
	} else {
		c, err = gosmtp.Dial(r.cfg.Addr)
	}
	if err != nil {
		return fmt.Errorf("relay connect: %w", err) // connection failure = transient error
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
		return fmt.Errorf("send body: %w", err)
	}
	if err := w.Close(); err != nil {
		return wrapSMTPErr(fmt.Errorf("finish body: %w", err), err)
	}
	return c.Quit()
}

// wrapSMTPErr promotes 5xx SMTP errors to PermanentError.
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

// ── DB-resolving Sender (0005) ───────────────────────────────────

// ResolvingSender resolves the sender domain's relay from the DB at send
// time. Domain-specific relay → default relay → error (retry).
// Changing the relay in the admin takes effect on the next send without a restart.
type ResolvingSender struct {
	store store.Store
}

// NewResolvingSender creates a DB-resolving sender.
func NewResolvingSender(st store.Store) *ResolvingSender {
	return &ResolvingSender{store: st}
}

var _ Sender = (*ResolvingSender)(nil)

// Send resolves the relay and then delegates to RelaySender.
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
		// no relay — an admin may add one soon, so retry as a transient error.
		return fmt.Errorf("no relay configured for domain %q (add a relay in the admin)", senderDomain)
	}
	return fmt.Errorf("relay resolve: %w", err)
}
