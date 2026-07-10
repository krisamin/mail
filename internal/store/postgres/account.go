package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/argon2"

	"github.com/krisamin/mail/internal/store"
)

// ErrNotFound is returned when the lookup target does not exist (alias of store.ErrNotFound — backward compat).
var ErrNotFound = store.ErrNotFound

// ErrAuthFailed is an authentication failure (alias of store.ErrAuthFailed — backward compat).
var ErrAuthFailed = store.ErrAuthFailed

// validLocalPart is a whitelist validation of the email local part (a subset of
// RFC 5321 dot-atom). It blocks control characters (\r\n — potential SMTP/header
// injection) and '@', spaces, '<', '>' and the like at the source. '*' is in
// atext but excluded here because it is the catch-all marker in this system
// (catch-all is only allowed as a standalone localPart == "*" at the call site).
// Assumes the input has already been lowercased.
func validLocalPart(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case strings.ContainsRune("!#$%&'+/=?^_`{|}~.-", c):
		default:
			return false
		}
	}
	return true
}

// splitAddress turns 'maro@krisam.in' → ('maro', 'krisam.in').
func splitAddress(address string) (local, domain string, err error) {
	at := strings.LastIndex(address, "@")
	if at < 0 {
		return "", "", fmt.Errorf("invalid address: %q", address)
	}
	return address[:at], address[at+1:], nil
}

// accountSelect is the shared SELECT for account lookups (0006 — identity model).
const accountSelect = `
	SELECT a.id, a.oidc_subject, COALESCE(a.oidc_email, ''), a.kind,
	       a.quota_bytes, a.active, a.created_at
	FROM account a`

func scanAccount(row pgx.Row) (*store.Account, error) {
	var u store.Account
	err := row.Scan(&u.ID, &u.OIDCSubject, &u.OIDCEmail, &u.Kind, &u.QuotaBytes, &u.Active, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("account lookup: %w", err)
	}
	return &u, nil
}

// FindDomain finds an active domain by name.
func (s *Store) FindDomain(ctx context.Context, name string) (*store.Domain, error) {
	const q = `
		SELECT id, name, active, created_at,
		       COALESCE(dkim_selector, ''), COALESCE(dkim_private_key, ''), relay_id
		FROM domain WHERE name = $1 AND active`
	var d store.Domain
	err := s.pool.QueryRow(ctx, q, name).Scan(
		&d.ID, &d.Name, &d.Active, &d.CreatedAt, &d.DKIMSelector, &d.DKIMPrivateKey, &d.RelayID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("domain lookup: %w", err)
	}
	return &d, nil
}

// FindAccountByAddress finds the active account that owns the address (exact
// match only — no wildcards). Used for IMAP/SMTP login and self-service mapping.
func (s *Store) FindAccountByAddress(ctx context.Context, address string) (*store.Account, error) {
	local, domain, err := splitAddress(strings.ToLower(address))
	if err != nil {
		return nil, err
	}
	const q = accountSelect + `
		JOIN address ad ON ad.account_id = a.id
		JOIN domain d ON d.id = ad.domain_id
		WHERE ad.local_part = $1 AND d.name = $2 AND a.active AND d.active`
	return scanAccount(s.pool.QueryRow(ctx, q, local, domain))
}

// FindAccountBySubject finds an active account by OIDC sub (web login identity).
func (s *Store) FindAccountBySubject(ctx context.Context, subject string) (*store.Account, error) {
	const q = accountSelect + ` WHERE a.oidc_subject = $1 AND a.active`
	return scanAccount(s.pool.QueryRow(ctx, q, subject))
}

// AuthenticateAppPassword authenticates with an address + app password.
// Compares against the account's non-revoked app passwords using argon2id.
func (s *Store) AuthenticateAppPassword(ctx context.Context, address, password string) (*store.Account, error) {
	u, err := s.FindAccountByAddress(ctx, address)
	if err != nil {
		return nil, err
	}
	const q = `
		SELECT id, hash FROM app_password
		WHERE account_id = $1 AND revoked_at IS NULL`
	rows, err := s.pool.Query(ctx, q, u.ID)
	if err != nil {
		return nil, fmt.Errorf("app password lookup: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var hash string
		if err := rows.Scan(&id, &hash); err != nil {
			return nil, err
		}
		if verifyPassword(password, hash) {
			// refresh last_used (best-effort)
			_, _ = s.pool.Exec(ctx, `UPDATE app_password SET last_used = now() WHERE id = $1`, id)
			return u, nil
		}
	}
	return nil, ErrAuthFailed
}

// ── argon2id helpers ────────────────────────────────────────
// Format: argon2id$<time>$<memoryKiB>$<threads>$<saltB64>$<hashB64>
// (minimal implementation for the spike. Parameters/format revisited in Phase 2)

const (
	argonTime    = 1
	argonMemory  = 64 * 1024 // 64MB
	argonThreads = 4
	argonKeyLen  = 32
)

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "argon2id" {
		return false
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return subtleConstEq(got, want)
}
