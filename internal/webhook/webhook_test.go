package webhook

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/krisamin/mail/internal/store/postgres"
)

func testEvent() Event {
	uid := uint32(7)
	return FromMailEvent(postgres.MailEvent{
		Kind: postgres.EventNew, AccountID: uuid.New(), Mailbox: "INBOX",
		UIDValidity: 1700000000, UID: &uid, OccurredAt: time.Now(),
	})
}

// noSleep records backoff waits instead of sleeping.
func noSleep(e *Emitter) *[]time.Duration {
	var waitList []time.Duration
	e.sleep = func(ctx context.Context, d time.Duration) bool {
		waitList = append(waitList, d)
		return true
	}
	return &waitList
}

// TestSignature — the receiver's recomputation over the received bytes
// matches; any change to body or secret does not.
func TestSignature(t *testing.T) {
	body := []byte(`{"eventId":"x"}`)
	sig := Sign("s3cret", body)
	if len(sig) != len("sha256=")+64 || sig[:7] != "sha256=" {
		t.Fatalf("signature format: %s", sig)
	}
	// known vector: HMAC-SHA256("s3cret", `{"eventId":"x"}`), computed independently
	if sig != "sha256=efb67b2c2ced617f60edeb035b9369fca6fb70e4597fa3b5758781b3ad9720bf" {
		t.Fatalf("signature %s does not match the reference vector", sig)
	}
	if !Verify("s3cret", body, sig) {
		t.Fatal("own signature must verify")
	}
	if Verify("s3cret", []byte(`{"eventId":"y"}`), sig) {
		t.Fatal("tampered body must not verify")
	}
	if Verify("other", body, sig) {
		t.Fatal("wrong secret must not verify")
	}
}

// TestDeliverSigned — one POST, signed over the exact body, event id header
// equal to the body's eventId, JSON shape as promised.
func TestDeliverSigned(t *testing.T) {
	var gotBody []byte
	var gotSig, gotID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotSig = r.Header.Get(HeaderSignature)
		gotID = r.Header.Get(HeaderEventID)
	}))
	defer srv.Close()
	e := New(Config{URL: srv.URL, Secret: "k"})
	ev := testEvent()
	if err := e.Deliver(context.Background(), ev); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if !Verify("k", gotBody, gotSig) {
		t.Fatalf("receiver could not verify signature %q over %s", gotSig, gotBody)
	}
	var m map[string]any
	if err := json.Unmarshal(gotBody, &m); err != nil {
		t.Fatalf("body: %v", err)
	}
	for _, k := range []string{"eventId", "kind", "accountId", "mailbox", "uidValidity", "uid", "occurredAt"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("body misses %s: %s", k, gotBody)
		}
	}
	if gotID != ev.EventID || m["eventId"] != ev.EventID || m["kind"] != "mail.new" || m["uid"].(float64) != 7 {
		t.Fatalf("id header %q / body %s", gotID, gotBody)
	}
}

// TestDeliverRetry — 5xx twice then 2xx: three attempts, backoff 1s then 2s.
func TestDeliverRetry(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) <= 2 {
			w.WriteHeader(503)
		}
	}))
	defer srv.Close()
	e := New(Config{URL: srv.URL, Secret: "k"})
	waitList := noSleep(e)
	if err := e.Deliver(context.Background(), testEvent()); err != nil {
		t.Fatalf("deliver after retries: %v", err)
	}
	if n != 3 || len(*waitList) != 2 || (*waitList)[0] != time.Second || (*waitList)[1] != 2*time.Second {
		t.Fatalf("attempts %d, waits %v", n, *waitList)
	}
}

// TestDeliverGiveUp — always failing: MaxAttempt tries, then dropped.
func TestDeliverGiveUp(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.WriteHeader(500)
	}))
	defer srv.Close()
	e := New(Config{URL: srv.URL, Secret: "k", MaxAttempt: 3})
	noSleep(e)
	if err := e.Deliver(context.Background(), testEvent()); err == nil {
		t.Fatal("expected give-up error")
	}
	if n != 3 {
		t.Fatalf("attempts %d, want 3", n)
	}
}

// TestDeliverNoRetryOn4xx — a 401 (receiver rejects the signature) is final;
// 429 is retried.
func TestDeliverNoRetryOn4xx(t *testing.T) {
	var n int32
	status := int32(401)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.WriteHeader(int(atomic.LoadInt32(&status)))
	}))
	defer srv.Close()
	e := New(Config{URL: srv.URL, Secret: "k"})
	noSleep(e)
	_ = e.Deliver(context.Background(), testEvent())
	if n != 1 {
		t.Fatalf("401 attempts %d, want 1", n)
	}
	atomic.StoreInt32(&n, 0)
	atomic.StoreInt32(&status, 429)
	_ = e.Deliver(context.Background(), testEvent())
	if n != 4 {
		t.Fatalf("429 attempts %d, want 4", n)
	}
}

// TestEnqueueOverflow — a full queue drops instead of blocking the caller.
func TestEnqueueOverflow(t *testing.T) {
	e := New(Config{URL: "http://127.0.0.1:1", Secret: "k", QueueSize: 1})
	done := make(chan struct{})
	go func() {
		e.Enqueue(testEvent())
		e.Enqueue(testEvent())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Enqueue blocked on a full queue")
	}
	if len(e.queue) != 1 {
		t.Fatalf("queue len %d, want 1", len(e.queue))
	}
}
