// Package relaysecret keeps relay SMTP passwords in OpenBao.
//
// Store wraps the database store: the relay CRUD methods write the password to
// OpenBao (<prefix>/<relay id>) and leave only a reference (relay.password_ref)
// in the database; ResolveRelay reads it back for a send. Every other store
// method passes through untouched. The admin API shape does not change — the
// password is still write-only and responses carry hasPassword only.
package relaysecret

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/krisamin/mail/internal/bao"
	"github.com/krisamin/mail/internal/store"
)

// Backend is the database store: store.Store (what the queue worker needs) plus
// the relay admin methods and SetRelayPasswordRef. postgres.Store implements it.
type Backend interface {
	store.Store
	ListRelay(ctx context.Context) ([]*store.Relay, error)
	CreateRelay(ctx context.Context, r *store.Relay) (*store.Relay, error)
	UpdateRelay(ctx context.Context, r *store.Relay) (*store.Relay, error)
	DeleteRelay(ctx context.Context, id uuid.UUID) error
	SetRelayPasswordRef(ctx context.Context, id uuid.UUID, ref string) error
}

// Vault is the part of the OpenBao client used here (fakeable in tests).
type Vault interface {
	Get(ctx context.Context, path string) (string, error)
	Put(ctx context.Context, path, value string) error
	Delete(ctx context.Context, path string) error
}

const (
	freshFor = time.Minute      // read-through cache lifetime
	staleFor = 10 * time.Minute // serve this old a value when OpenBao is unreachable
)

// Store is the wrapping store.
type Store struct {
	Backend
	vault  Vault
	prefix string

	mu    sync.Mutex
	cache map[string]cached
	now   func() time.Time
}

type cached struct {
	value string
	at    time.Time
}

// Wrap returns a store whose relay passwords live in vault under prefix
// (e.g. "mail/relay" → "mail/relay/<relay id>").
func Wrap(b Backend, v Vault, prefix string) *Store {
	return &Store{
		Backend: b, vault: v,
		prefix: strings.Trim(prefix, "/"),
		cache:  map[string]cached{},
		now:    time.Now,
	}
}

// SetClockForTest moves the cache's notion of "now" (tests only).
func (s *Store) SetClockForTest(t time.Time) {
	s.mu.Lock()
	s.now = func() time.Time { return t }
	s.mu.Unlock()
}

func (s *Store) refFor(id uuid.UUID) string { return s.prefix + "/" + id.String() }

func (s *Store) forget(ref string) {
	s.mu.Lock()
	delete(s.cache, ref)
	s.mu.Unlock()
}

// read returns the secret at ref: fresh cache → OpenBao → (on failure) a stale
// cache entry, so a short OpenBao outage does not stop outbound mail.
func (s *Store) read(ctx context.Context, ref string) (string, error) {
	s.mu.Lock()
	c, ok := s.cache[ref]
	now := s.now()
	s.mu.Unlock()
	if ok && now.Sub(c.at) < freshFor {
		return c.value, nil
	}
	v, err := s.vault.Get(ctx, ref)
	if err != nil {
		if ok && !errors.Is(err, bao.ErrNotFound) && now.Sub(c.at) < staleFor {
			log.Printf("relaysecret: OpenBao read failed, using cached value (%v)", err)
			return c.value, nil
		}
		return "", fmt.Errorf("relay password: %w", err)
	}
	s.mu.Lock()
	s.cache[ref] = cached{value: v, at: now}
	s.mu.Unlock()
	return v, nil
}

// ResolveRelay finds the relay for the sender domain and fills Password from
// OpenBao when the row only holds a reference.
func (s *Store) ResolveRelay(ctx context.Context, senderDomain string) (*store.Relay, error) {
	r, err := s.Backend.ResolveRelay(ctx, senderDomain)
	if err != nil {
		return nil, err
	}
	if r.PasswordRef != "" {
		pw, err := s.read(ctx, r.PasswordRef)
		if err != nil {
			return nil, err
		}
		r.Password = pw
	}
	return r, nil
}

// CreateRelay writes the password to OpenBao first (under a fresh id), then
// inserts the row with only the reference. A failed insert removes the secret.
func (s *Store) CreateRelay(ctx context.Context, r *store.Relay) (*store.Relay, error) {
	if r.Password == "" {
		return s.Backend.CreateRelay(ctx, r)
	}
	pw := r.Password
	id := uuid.New()
	ref := s.refFor(id)
	if err := s.vault.Put(ctx, ref, pw); err != nil {
		return nil, fmt.Errorf("relay password: %w", err)
	}
	cp := *r
	cp.ID, cp.PasswordRef, cp.Password = id, ref, ""
	out, err := s.Backend.CreateRelay(ctx, &cp)
	if err != nil {
		if derr := s.vault.Delete(ctx, ref); derr != nil {
			log.Printf("relaysecret: cleanup of %s failed: %v", ref, derr)
		}
		return nil, err
	}
	s.forget(ref)
	return out, nil
}

// UpdateRelay writes a new password to OpenBao before the row changes. An
// empty password keeps whatever is stored (reference or legacy plaintext).
func (s *Store) UpdateRelay(ctx context.Context, r *store.Relay) (*store.Relay, error) {
	if r.Password == "" {
		return s.Backend.UpdateRelay(ctx, r)
	}
	ref := s.refFor(r.ID)
	if err := s.vault.Put(ctx, ref, r.Password); err != nil {
		return nil, fmt.Errorf("relay password: %w", err)
	}
	s.forget(ref)
	cp := *r
	cp.Password, cp.PasswordRef = "", ref
	return s.Backend.UpdateRelay(ctx, &cp)
}

// DeleteRelay deletes the row, then the OpenBao secret it pointed at.
func (s *Store) DeleteRelay(ctx context.Context, id uuid.UUID) error {
	var ref string
	if list, err := s.Backend.ListRelay(ctx); err == nil {
		for _, r := range list {
			if r.ID == id {
				ref = r.PasswordRef
			}
		}
	}
	if err := s.Backend.DeleteRelay(ctx, id); err != nil {
		return err
	}
	if ref != "" {
		s.forget(ref)
		if err := s.vault.Delete(ctx, ref); err != nil {
			// the relay is gone either way; an orphan secret is harmless but worth a line
			log.Printf("relaysecret: relay deleted but OpenBao secret %s was not: %v", ref, err)
		}
	}
	return nil
}

// MoveLegacy moves every relay that still has a plaintext password into
// OpenBao and blanks the database column. For each one it writes, reads the
// value back and compares, and only then points the row at the secret and
// clears the column. Idempotent: rows already moved are skipped. One failing
// relay does not stop the others; the error (names only, never values) lists
// what is left. Returns how many moved.
func (s *Store) MoveLegacy(ctx context.Context) (int, error) {
	list, err := s.Backend.ListRelay(ctx)
	if err != nil {
		return 0, err
	}
	moved := 0
	var failed []string
	for _, r := range list {
		if r.Password == "" {
			continue
		}
		ref := s.refFor(r.ID)
		if err := s.moveOne(ctx, r, ref); err != nil {
			log.Printf("relaysecret: relay %q not moved: %v", r.Name, err)
			failed = append(failed, r.Name)
			continue
		}
		log.Printf("relaysecret: relay %q password moved to OpenBao (%s), database column blanked", r.Name, ref)
		moved++
	}
	if len(failed) > 0 {
		return moved, fmt.Errorf("relays still in plaintext: %s", strings.Join(failed, ", "))
	}
	return moved, nil
}

func (s *Store) moveOne(ctx context.Context, r *store.Relay, ref string) error {
	if err := s.vault.Put(ctx, ref, r.Password); err != nil {
		return err
	}
	got, err := s.vault.Get(ctx, ref)
	if err != nil {
		return fmt.Errorf("read-back: %w", err)
	}
	if got != r.Password {
		return errors.New("read-back differs from what was written")
	}
	s.forget(ref)
	return s.Backend.SetRelayPasswordRef(ctx, r.ID, ref)
}
