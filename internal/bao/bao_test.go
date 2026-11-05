package bao_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/krisamin/mail/internal/bao"
	"github.com/krisamin/mail/internal/bao/baotest"
)

func TestClientRoundTrip(t *testing.T) {
	srv := baotest.New(t)
	c := srv.Client(t)
	ctx := context.Background()

	if _, err := c.Get(ctx, "mail/relay/a"); !errors.Is(err, bao.ErrNotFound) {
		t.Fatalf("missing secret: want ErrNotFound, got %v", err)
	}
	if err := c.Put(ctx, "mail/relay/a", "re_s3cret"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if v, ok := srv.Value("mail/relay/a"); !ok || v != "re_s3cret" {
		t.Fatalf("stored value wrong: %q %v", v, ok)
	}
	got, err := c.Get(ctx, "mail/relay/a")
	if err != nil || got != "re_s3cret" {
		t.Fatalf("get: %q %v", got, err)
	}
	if err := c.Delete(ctx, "mail/relay/a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := srv.Value("mail/relay/a"); ok {
		t.Fatal("secret still there after delete")
	}
	if err := c.Delete(ctx, "mail/relay/a"); err != nil {
		t.Fatalf("deleting a missing secret must not fail: %v", err)
	}
	// one login served every call above
	if srv.Logins != 1 {
		t.Fatalf("logins = %d, want 1 (token cached)", srv.Logins)
	}
}

func TestClientRelogsInOnForbidden(t *testing.T) {
	srv := baotest.New(t)
	c := srv.Client(t)
	ctx := context.Background()
	if err := c.Put(ctx, "p/x", "v1"); err != nil {
		t.Fatal(err)
	}
	srv.Revoke() // token revoked early
	got, err := c.Get(ctx, "p/x")
	if err != nil || got != "v1" {
		t.Fatalf("get after revoke: %q %v", got, err)
	}
	if srv.Logins != 2 {
		t.Fatalf("logins = %d, want 2", srv.Logins)
	}
}

func TestClientLoginFailureAndErrors(t *testing.T) {
	srv := baotest.New(t)
	bad, err := bao.New(bao.Config{Addr: srv.URL, Role: "other", Mount: baotest.Mount, JWTPath: srv.JWTPath})
	if err != nil {
		t.Fatal(err)
	}
	err = bad.Put(context.Background(), "p/x", "SUPERSECRET")
	if err == nil || !strings.Contains(err.Error(), "login") {
		t.Fatalf("wrong role must fail at login, got %v", err)
	}
	if strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatal("error leaks the secret")
	}
	// server error on KV: status in the message, no value
	c := srv.Client(t)
	srv.Down = true
	err = c.Put(context.Background(), "p/x", "SUPERSECRET")
	if err == nil || !strings.Contains(err.Error(), "503") || strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatalf("down: %v", err)
	}
}

func TestClientConfigValidation(t *testing.T) {
	if _, err := bao.New(bao.Config{Role: "r"}); err == nil {
		t.Fatal("addr required")
	}
	if _, err := bao.New(bao.Config{Addr: "http://x"}); err == nil {
		t.Fatal("role required")
	}
}

func TestClientRenewsBeforeLeaseEnds(t *testing.T) {
	srv := baotest.New(t)
	srv.Lease = 1 // 1s lease → renewed after 0.8s
	c := srv.Client(t)
	ctx := context.Background()
	if err := c.Put(ctx, "p/x", "v"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(900 * time.Millisecond)
	if _, err := c.Get(ctx, "p/x"); err != nil {
		t.Fatal(err)
	}
	if srv.Logins != 2 {
		t.Fatalf("logins = %d, want 2 (renewed at 80%% of lease)", srv.Logins)
	}
}
