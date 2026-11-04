// Package webhook pushes mail events (new mail, flag changes, expunges) to
// one HTTP endpoint — the ara server's maild adapter.
//
// Semantics: a webhook is a poke, not a log. It tells the receiver "this
// mailbox of this account changed, come and look"; the receiver then pulls
// the truth through the internal sync API using its own UID cursor. So an
// event may be lost (LISTEN connection dropped, queue overflow, endpoint down
// longer than the retry budget) and nothing breaks: the next event, or the
// receiver's own periodic sync, catches up. Delivery is therefore
// best-effort: a few retries with exponential backoff, then the event is
// dropped and counted (mail_event_webhook_total{outcome="dropped"}).
//
// Authenticity: every request carries X-Mail-Signature: sha256=<hex> where
// hex is HMAC-SHA256(secret, raw body). The receiver must recompute it over
// the exact bytes it received and compare in constant time. X-Mail-Event-Id
// repeats the body's eventId so retries of one event can be de-duplicated.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/krisamin/mail/internal/metric"
	"github.com/krisamin/mail/internal/store/postgres"
)

// Header names.
const (
	HeaderSignature = "X-Mail-Signature"
	HeaderEventID   = "X-Mail-Event-Id"
)

// Config configures the emitter. URL and Secret are required.
type Config struct {
	URL    string
	Secret string
	// MaxAttempt is the total number of tries per event (default 4).
	MaxAttempt int
	// BaseBackoff is the wait before the 2nd try; it doubles each time
	// (default 1s → 1s, 2s, 4s).
	BaseBackoff time.Duration
	// QueueSize bounds the pending events (default 1024); overflow is dropped.
	QueueSize int
	// Client is the HTTP client (default: 5s timeout).
	Client *http.Client
}

// Event is the wire body.
type Event struct {
	EventID     string  `json:"eventId"`
	Kind        string  `json:"kind"`
	AccountID   string  `json:"accountId"`
	Mailbox     string  `json:"mailbox"`
	UIDValidity uint32  `json:"uidValidity"`
	UID         *uint32 `json:"uid,omitempty"`
	OccurredAt  string  `json:"occurredAt"`
}

// FromMailEvent converts a store notification into a wire event with a fresh id.
func FromMailEvent(ev postgres.MailEvent) Event {
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	at := ev.OccurredAt
	if at.IsZero() {
		at = time.Now()
	}
	return Event{
		EventID:     id.String(),
		Kind:        ev.Kind,
		AccountID:   ev.AccountID.String(),
		Mailbox:     ev.Mailbox,
		UIDValidity: ev.UIDValidity,
		UID:         ev.UID,
		OccurredAt:  at.UTC().Format(time.RFC3339Nano),
	}
}

// Sign returns the X-Mail-Signature value for body.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify checks a signature header against body in constant time.
// Receivers (and tests) use it; maild itself only signs.
func Verify(secret string, body []byte, header string) bool {
	return hmac.Equal([]byte(Sign(secret, body)), []byte(header))
}

// Emitter queues events and delivers them one at a time.
type Emitter struct {
	cfg   Config
	queue chan Event
	// sleep is swapped out by tests to skip real backoff waits.
	sleep func(ctx context.Context, d time.Duration) bool
}

// New builds an emitter. Call Run in a goroutine.
func New(cfg Config) *Emitter {
	if cfg.MaxAttempt <= 0 {
		cfg.MaxAttempt = 4
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = time.Second
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 1024
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 5 * time.Second}
	}
	return &Emitter{cfg: cfg, queue: make(chan Event, cfg.QueueSize), sleep: sleepCtx}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Enqueue adds an event without blocking; a full queue drops it.
func (e *Emitter) Enqueue(ev Event) {
	select {
	case e.queue <- ev:
	default:
		metric.EventWebhookTotal.WithLabelValues("overflow").Inc()
		log.Printf("webhook: queue full, dropped %s %s", ev.Kind, ev.EventID)
	}
}

// Pump forwards store notifications into the queue until ctx ends.
func (e *Emitter) Pump(ctx context.Context, src <-chan postgres.MailEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-src:
			e.Enqueue(FromMailEvent(ev))
		}
	}
}

// Run delivers queued events until ctx ends.
func (e *Emitter) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-e.queue:
			_ = e.Deliver(ctx, ev)
		}
	}
}

// Deliver sends one event with retries. Returns nil when the endpoint
// answered 2xx, otherwise the last error (the event is then dropped).
func (e *Emitter) Deliver(ctx context.Context, ev Event) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	backoff := e.cfg.BaseBackoff
	var lastErr error
	for attempt := 1; attempt <= e.cfg.MaxAttempt; attempt++ {
		retryable, err := e.post(ctx, ev.EventID, body)
		if err == nil {
			metric.EventWebhookTotal.WithLabelValues("delivered").Inc()
			return nil
		}
		lastErr = err
		if !retryable || attempt == e.cfg.MaxAttempt {
			break
		}
		metric.EventWebhookTotal.WithLabelValues("retry").Inc()
		if !e.sleep(ctx, backoff) {
			break
		}
		backoff *= 2
	}
	metric.EventWebhookTotal.WithLabelValues("dropped").Inc()
	log.Printf("webhook: dropped %s %s: %v", ev.Kind, ev.EventID, lastErr)
	return lastErr
}

// post makes one attempt. retryable is false for answers that a retry
// cannot fix (4xx other than 408/429 — e.g. a signature the receiver rejects).
func (e *Emitter) post(ctx context.Context, eventID string, body []byte) (retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderSignature, Sign(e.cfg.Secret, body))
	req.Header.Set(HeaderEventID, eventID)
	resp, err := e.cfg.Client.Do(req)
	if err != nil {
		return true, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return false, nil
	}
	err = fmt.Errorf("status %d", resp.StatusCode)
	switch {
	case resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooManyRequests:
		return true, err
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return false, err
	default:
		return true, err
	}
}
