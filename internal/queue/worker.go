// Package queue implements the outbound queue worker (Phase 2-3).
//
// When submission puts external-domain recipients into the store's
// outbound_queue, the worker periodically picks up due entries and sends
// them via a Sender.
//
// ★Sending policy (DD-04): no direct MX sending. The default Sender
// implementation goes through an SMTP relay (SES/Postmark etc.) — the relay
// choice is still undecided, so it's abstracted behind the Sender interface.
// Fill in the config and it plugs in.
package queue

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/krisamin/mail/internal/metric"
	"github.com/krisamin/mail/internal/store"
)

// Sender is responsible for actually sending one message. Implementations:
//   - RelaySender: via SMTP relay (production default)
//   - tests: mock
type Sender interface {
	// Send sends using the envelope from/rcpt and the raw message.
	// Returning a PermanentError marks it failed immediately without retry.
	Send(ctx context.Context, from, rcpt string, raw []byte) error
}

// PermanentError is a failure that retrying won't fix (5xx etc.).
type PermanentError struct {
	Err error
}

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Config holds the worker behavior parameters.
type Config struct {
	// PollInterval is the due-scan interval. Default 10s.
	PollInterval time.Duration
	// BatchSize is the max number of entries fetched at once. Default 10.
	BatchSize int
	// Exceeding MaxAttempts means permanent failure. Default 6 (total backoff ≈ 2 hours).
	MaxAttemptCount int
	// BaseBackoff is the base of the exponential backoff. Default 1 minute: 1m→2m→4m→8m→16m→32m.
	BaseBackoff time.Duration
}

func (c Config) withDefaults() Config {
	if c.PollInterval <= 0 {
		c.PollInterval = 10 * time.Second
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 10
	}
	if c.MaxAttemptCount <= 0 {
		c.MaxAttemptCount = 6
	}
	if c.BaseBackoff <= 0 {
		c.BaseBackoff = time.Minute
	}
	return c
}

// Worker consumes the outbound queue.
type Worker struct {
	store  store.Store
	sender Sender
	cfg    Config
	// signer is the pre-send DKIM signing hook (nil sends unsigned).
	signer SignFunc
	// hostname is for the DSN's Reporting-MTA/mailer-daemon address.
	hostname string
}

// SignFunc signs the message right before sending. On failure the raw
// message is sent as-is (signing is best-effort — a signing failure doesn't
// block sending).
type SignFunc func(ctx context.Context, envelopeFrom string, raw []byte) ([]byte, error)

// NewWorker creates a worker.
func NewWorker(st store.Store, sender Sender, cfg Config) *Worker {
	return &Worker{store: st, sender: sender, cfg: cfg.withDefaults()}
}

// WithSigner attaches the DKIM signing hook.
func (w *Worker) WithSigner(f SignFunc) *Worker {
	w.signer = f
	return w
}

// WithHostname attaches the hostname used for DSN generation (unset disables DSN).
func (w *Worker) WithHostname(hostname string) *Worker {
	w.hostname = hostname
	return w
}

// Run processes the queue periodically until ctx is canceled.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	log.Printf("queue: outbound worker started (poll=%s batch=%d maxAttemptCount=%d)",
		w.cfg.PollInterval, w.cfg.BatchSize, w.cfg.MaxAttemptCount)
	for {
		select {
		case <-ctx.Done():
			log.Printf("queue: outbound worker stopped")
			return
		case <-ticker.C:
			if n, err := w.ProcessOnce(ctx); err != nil {
				log.Printf("queue: processing error: %v", err)
			} else if n > 0 {
				log.Printf("queue: processed %d entries", n)
			}
		}
	}
}

// ProcessOnce processes one batch of due entries and returns the count.
// (The unit shared by tests and the Run loop.)
func (w *Worker) ProcessOnce(ctx context.Context) (int, error) {
	due, err := w.store.DueOutbound(ctx, w.cfg.BatchSize)
	if err != nil {
		return 0, fmt.Errorf("due lookup: %w", err)
	}
	if statStore, ok := w.store.(interface {
		OutboundStat(ctx context.Context) (map[string]int64, error)
	}); ok {
		if stat, serr := statStore.OutboundStat(ctx); serr == nil {
			metric.QueuePendingGauge.Set(float64(stat[store.OutboundPending]))
		}
	}

	for _, m := range due {
		w.processMessage(ctx, m)
	}
	return len(due), nil
}

func (w *Worker) processMessage(ctx context.Context, m *store.OutboundMessage) {
	// per-message timeout — a hung relay TCP connection would stall the whole
	// batch (serial loop), so cap it to keep one message from holding the
	// worker hostage.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	raw := m.Raw
	if w.signer != nil {
		signed, err := w.signer(ctx, m.EnvelopeFrom, raw)
		if err != nil {
			log.Printf("queue: DKIM signing failed id=%s (sending unsigned): %v", m.ID, err)
		} else {
			raw = signed
		}
	}

	sendStart := time.Now()
	err := w.sender.Send(ctx, m.EnvelopeFrom, m.EnvelopeRcpt, raw)
	metric.SendDuration.Observe(time.Since(sendStart).Seconds())
	if err == nil {
		if err := w.store.MarkOutboundSent(ctx, m.ID); err != nil {
			log.Printf("queue: marking sent failed id=%s: %v", m.ID, err)
		}
		metric.QueueSendTotal.WithLabelValues("sent").Inc()
		log.Printf("queue: sent id=%s to=%s (attempt %d)", m.ID, m.EnvelopeRcpt, m.AttemptCount+1)
		return
	}

	// permanent error or retries exhausted → failed
	var perm *PermanentError
	if errors.As(err, &perm) || m.AttemptCount+1 >= w.cfg.MaxAttemptCount {
		if merr := w.store.MarkOutboundFailed(ctx, m.ID, err.Error()); merr != nil {
			log.Printf("queue: marking failed failed id=%s: %v", m.ID, merr)
		}
		metric.QueueSendTotal.WithLabelValues("failed").Inc()
		log.Printf("queue: permanent failure id=%s to=%s: %v (attempt %d/%d)",
			m.ID, m.EnvelopeRcpt, err, m.AttemptCount+1, w.cfg.MaxAttemptCount)
		// bounce DSN to the sender (RFC 3464) — the sender is a local user, so straight to INBOX
		if w.hostname != "" {
			w.deliverDSN(ctx, m, err.Error())
		}
		return
	}

	// transient error → exponential backoff retry (capped at 1 hour — a large
	// AttemptCount overflows the int64 shift into a negative/zero backoff →
	// an immediate-retry hot loop)
	const maxBackoff = time.Hour
	backoff := w.cfg.BaseBackoff
	for i := 0; i < m.AttemptCount && backoff < maxBackoff; i++ {
		backoff <<= 1
	}
	if backoff <= 0 || backoff > maxBackoff {
		backoff = maxBackoff
	}
	next := time.Now().Add(backoff)
	if merr := w.store.MarkOutboundRetry(ctx, m.ID, err.Error(), next); merr != nil {
		log.Printf("queue: marking retry failed id=%s: %v", m.ID, merr)
	}
	metric.QueueSendTotal.WithLabelValues("retry").Inc()
	log.Printf("queue: retry scheduled id=%s to=%s in %s: %v (attempt %d/%d)",
		m.ID, m.EnvelopeRcpt, backoff, err, m.AttemptCount+1, w.cfg.MaxAttemptCount)
}
