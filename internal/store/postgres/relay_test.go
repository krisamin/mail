package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/krisamin/mail/internal/store"
)

// TestRelay verifies relay CRUD + ResolveRelay priority.
// Required environment: MAIL_TEST_DSN (same as store_test.go)
func TestRelay(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	_, _ = s.pool.Exec(ctx, `TRUNCATE domain, account, app_password, mailbox, message, message_flag, message_blob, outbound_queue, address, relay RESTART IDENTITY CASCADE`)

	// seed: two domains
	var krisamID, kirbyID int64
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO domain (name) VALUES ('krisam.in') RETURNING id`).Scan(&krisamID); err != nil {
		t.Fatalf("domain seed: %v", err)
	}
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO domain (name) VALUES ('kirby.so') RETURNING id`).Scan(&kirbyID); err != nil {
		t.Fatalf("domain seed: %v", err)
	}

	// 1) no relay → ErrNotFound
	if _, err := s.ResolveRelay(ctx, "krisam.in"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound when no relay exists, got %v", err)
	}
	t.Log("✔ no relay → ErrNotFound")

	// 2) create default relay → any domain resolves to default
	resend, err := s.CreateRelay(ctx, &store.Relay{
		Name: "resend", Host: "smtp.resend.com", Port: 587,
		Username: "resend", Password: "re_secret", StartTLS: true,
		IsDefault: true, Active: true,
	})
	if err != nil {
		t.Fatalf("relay creation: %v", err)
	}
	got, err := s.ResolveRelay(ctx, "krisam.in")
	if err != nil || got.ID != resend.ID {
		t.Fatalf("default resolution failed: %v %+v", err, got)
	}
	t.Log("✔ default relay resolution")

	// 3) domain-specific relay takes priority over default
	ses, err := s.CreateRelay(ctx, &store.Relay{
		Name: "ses", Host: "email-smtp.ap-northeast-2.amazonaws.com", Port: 587,
		Username: "AKIA...", Password: "sespw", StartTLS: true, Active: true,
	})
	if err != nil {
		t.Fatalf("relay creation: %v", err)
	}
	if err := s.SetDomainRelay(ctx, kirbyID, &ses.ID); err != nil {
		t.Fatalf("domain relay assignment: %v", err)
	}
	got, err = s.ResolveRelay(ctx, "kirby.so")
	if err != nil || got.ID != ses.ID {
		t.Fatalf("domain-specific resolution failed: %v %+v", err, got)
	}
	got, err = s.ResolveRelay(ctx, "krisam.in") // unassigned domain still resolves to default
	if err != nil || got.ID != resend.ID {
		t.Fatalf("unassigned domain default resolution failed: %v %+v", err, got)
	}
	t.Log("✔ domain-specific > default priority")

	// 4) inactive relay is ignored
	ses.Active = false
	if _, err := s.UpdateRelay(ctx, ses); err != nil {
		t.Fatalf("relay update: %v", err)
	}
	got, err = s.ResolveRelay(ctx, "kirby.so")
	if err != nil || got.ID != resend.ID {
		t.Fatalf("inactive ignore failed (should fall back to default): %v %+v", err, got)
	}
	t.Log("✔ inactive relay ignored → default")

	// 5) UpdateRelay empty password string = keep existing value
	resend.Password = ""
	resend.Host = "smtp2.resend.com"
	updated, err := s.UpdateRelay(ctx, resend)
	if err != nil {
		t.Fatalf("relay update: %v", err)
	}
	if updated.Password != "re_secret" || updated.Host != "smtp2.resend.com" {
		t.Fatalf("password retention failed: %+v", updated)
	}
	t.Log("✔ empty password string = existing value kept")

	// 6) default handover: make ses default → resend loses default
	ses.Active = true
	ses.IsDefault = true
	if _, err := s.UpdateRelay(ctx, ses); err != nil {
		t.Fatalf("default handover: %v", err)
	}
	relayList, err := s.ListRelay(ctx)
	if err != nil {
		t.Fatalf("relay list: %v", err)
	}
	defaultCount := 0
	for _, r := range relayList {
		if r.IsDefault {
			defaultCount++
		}
	}
	if defaultCount != 1 {
		t.Fatalf("there must be exactly one default, got %d", defaultCount)
	}
	t.Log("✔ default uniqueness (previous cleared on handover)")

	// 7) delete → domain relay_id is SET NULL
	if err := s.DeleteRelay(ctx, ses.ID); err != nil {
		t.Fatalf("relay delete: %v", err)
	}
	var relayID *int64
	if err := s.pool.QueryRow(ctx,
		`SELECT relay_id FROM domain WHERE id = $1`, kirbyID).Scan(&relayID); err != nil {
		t.Fatalf("domain lookup: %v", err)
	}
	if relayID != nil {
		t.Fatalf("relay_id must be NULL after delete, got %v", *relayID)
	}
	t.Log("✔ relay delete → domain.relay_id SET NULL")
}
