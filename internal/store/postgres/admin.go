package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/krisamin/mail/internal/store"
)

// AdminStore implementation (Phase 3). Compile-time check is against store.AdminStore.
var _ store.AdminStore = (*Store)(nil)

// ── Domains ─────────────────────────────────────────────────

// ListDomain lists all domains (including inactive — for the admin screen).
func (s *Store) ListDomain(ctx context.Context) ([]*store.Domain, error) {
	const q = `
		SELECT id, name, active, created_at,
		       COALESCE(dkim_selector, ''), COALESCE(dkim_private_key, ''), relay_id
		FROM domain ORDER BY name`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("domain list: %w", err)
	}
	defer rows.Close()

	var out []*store.Domain
	for rows.Next() {
		var d store.Domain
		if err := rows.Scan(&d.ID, &d.Name, &d.Active, &d.CreatedAt,
			&d.DKIMSelector, &d.DKIMPrivateKey, &d.RelayID); err != nil {
			return nil, err
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

// CreateDomain creates a new domain. Lowercase-normalized.
func (s *Store) CreateDomain(ctx context.Context, name string) (*store.Domain, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || !strings.Contains(name, ".") {
		return nil, fmt.Errorf("invalid domain name: %q", name)
	}
	const q = `
		INSERT INTO domain (name) VALUES ($1)
		RETURNING id, name, active, created_at`
	var d store.Domain
	err := s.pool.QueryRow(ctx, q, name).Scan(&d.ID, &d.Name, &d.Active, &d.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("domain create: %w", err)
	}
	return &d, nil
}

// BackfillDomainAddress retroactively creates the primary address + INBOX for
// existing human accounts whose oidc_email is on this domain (called right
// after adding a domain). Accounts that already have the address (including
// ones owned by another account) and service accounts are skipped. Idempotent.
// Returns the number of addresses created.
func (s *Store) BackfillDomainAddress(ctx context.Context, domainID int64) (int, error) {
	// verify the domain
	var domainName string
	err := s.pool.QueryRow(ctx,
		`SELECT name FROM domain WHERE id = $1`, domainID).Scan(&domainName)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("domain lookup: %w", err)
	}

	// targets: active human accounts with oidc_email @<domain> that don't have the address yet
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, lower(split_part(a.oidc_email, '@', 1))
		FROM account a
		WHERE a.kind = 'user' AND a.active
		  AND lower(split_part(a.oidc_email, '@', 2)) = $1
		  AND NOT EXISTS (
		    SELECT 1 FROM address x
		    WHERE x.domain_id = $2
		      AND x.local_part = lower(split_part(a.oidc_email, '@', 1)))
		ORDER BY a.id`, domainName, domainID)
	if err != nil {
		return 0, fmt.Errorf("backfill target lookup: %w", err)
	}
	type target struct {
		accountID int64
		local     string
	}
	var targetList []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.accountID, &t.local); err != nil {
			rows.Close()
			return 0, err
		}
		targetList = append(targetList, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	created := 0
	for _, t := range targetList {
		if t.local == "" || t.local == "*" {
			continue
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return created, err
		}
		// ON CONFLICT DO NOTHING — if the address appeared between the target
		// query and the INSERT, or someone else claimed the same address, that
		// one row must not block the backfill of all remaining accounts (fixes
		// a bug where this was fail-fast). Conflicting rows are skipped and we
		// keep going — idempotent re-runs follow naturally.
		tag, err := tx.Exec(ctx,
			`INSERT INTO address (domain_id, local_part, account_id) VALUES ($1, $2, $3)
			 ON CONFLICT (domain_id, local_part) DO NOTHING`,
			domainID, t.local, t.accountID)
		if err != nil {
			tx.Rollback(ctx)
			return created, fmt.Errorf("backfill address create(%s@%s): %w", t.local, domainName, err)
		}
		if tag.RowsAffected() == 0 {
			// already exists/claimed — skip
			tx.Rollback(ctx)
			continue
		}
		// create INBOX if missing (bare accounts have no INBOX either)
		if _, err := tx.Exec(ctx,
			`INSERT INTO mailbox (account_id, name, uid_validity, uid_next, subscribed)
			 VALUES ($1, 'INBOX', $2, 1, true)
			 ON CONFLICT (account_id, name) DO NOTHING`,
			t.accountID, newUIDValidity()); err != nil {
			tx.Rollback(ctx)
			return created, fmt.Errorf("backfill INBOX create: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

// SetDomainActive changes the domain's active state.
func (s *Store) SetDomainActive(ctx context.Context, id int64, active bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE domain SET active = $2 WHERE id = $1`, id, active)
	if err != nil {
		return fmt.Errorf("domain state change: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetDomainDKIM changes the DKIM configuration. Empty selector = unset.
func (s *Store) SetDomainDKIM(ctx context.Context, id int64, selector, privateKeyPEM string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE domain SET dkim_selector = NULLIF($2, ''), dkim_private_key = NULLIF($3, '')
		 WHERE id = $1`, id, selector, privateKeyPEM)
	if err != nil {
		return fmt.Errorf("DKIM config: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── Accounts ────────────────────────────────────────────────

// ListAccount lists all accounts (including inactive — for the admin screen).
func (s *Store) ListAccount(ctx context.Context) ([]*store.Account, error) {
	const q = `
		SELECT id, oidc_subject, COALESCE(oidc_email, ''), kind,
		       quota_bytes, active, created_at
		FROM account ORDER BY kind, oidc_email`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("account list: %w", err)
	}
	defer rows.Close()

	var out []*store.Account
	for rows.Next() {
		var u store.Account
		if err := rows.Scan(&u.ID, &u.OIDCSubject, &u.OIDCEmail, &u.Kind,
			&u.QuotaBytes, &u.Active, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &u)
	}
	return out, rows.Err()
}

// ProvisionAccount does JIT provisioning keyed by OIDC sub (idempotent).
//   - existing account found by sub: refresh oidc_email and return it (addresses
//     untouched — even if the email changes at the IdP, existing addresses are
//     managed by the admin)
//   - not found: create the account. If the email domain is registered, also
//     create the primary address + INBOX; if not, the account only (no address
//     = mail unusable. When the admin later adds the domain, backfill creates
//     the address).
//   - if the email address is already owned by another account: duplicate error
//     (a conflict — a state the admin must clean up).
func (s *Store) ProvisionAccount(ctx context.Context, subject, email string) (*store.Account, error) {
	subject = strings.TrimSpace(subject)
	email = strings.ToLower(strings.TrimSpace(email))
	if subject == "" {
		return nil, fmt.Errorf("invalid identity: empty sub")
	}

	// existing account — refresh email only (inactive accounts are excluded since blocking login is the point)
	if u, err := s.FindAccountBySubject(ctx, subject); err == nil {
		if u.OIDCEmail != email {
			if _, err := s.pool.Exec(ctx,
				`UPDATE account SET oidc_email = $2 WHERE id = $1`, u.ID, email); err != nil {
				return nil, fmt.Errorf("account email update: %w", err)
			}
			u.OIDCEmail = email
		}
		return u, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	// Adoption: if an existing human account has the same email as primary
	// (a placeholder sub from seeding/migration, or the sub changed because
	// the user was recreated at the IdP), take it over by updating the sub.
	// ★Service accounts cannot be adopted — creating a user with the same
	// email at the IdP cannot hijack a service account.
	// Adoption is also forbidden when the address exists but the owning
	// account's oidc_email differs (someone else's extra address) — the
	// INSERT below fails as a duplicate.
	if owner, err := s.FindAccountByAddress(ctx, email); err == nil &&
		owner.OIDCEmail == email && owner.Kind == store.AccountKindUser {
		if _, err := s.pool.Exec(ctx,
			`UPDATE account SET oidc_subject = $2 WHERE id = $1`, owner.ID, subject); err != nil {
			return nil, fmt.Errorf("account identity update: %w", err)
		}
		owner.OIDCSubject = subject
		return owner, nil
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	u, err := s.createAccountWithAddress(ctx, subject, email, store.AccountKindUser)
	if errors.Is(err, store.ErrNotFound) {
		// domain not registered — create the account only (no address/INBOX = mail unusable).
		// When the admin adds the domain later, BackfillDomainAddress fills it in.
		u, err = s.createBareAccount(ctx, subject, email, store.AccountKindUser)
	}
	if err != nil && isUniqueViolation(err) {
		// Concurrent JIT provisioning (same user logging in from two tabs, etc.) —
		// if both decide "no account" and race on the INSERT, one loses with a
		// duplicate. The loser re-fetches the account the winner created and
		// returns it — idempotent.
		if existing, ferr := s.FindAccountBySubject(ctx, subject); ferr == nil {
			return existing, nil
		}
		return nil, err
	}
	return u, err
}

// isUniqueViolation reports a pg unique_violation (23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// createBareAccount creates just an account without an address (login for users of unregistered domains).
func (s *Store) createBareAccount(ctx context.Context, subject, email, kind string) (*store.Account, error) {
	var u store.Account
	err := s.pool.QueryRow(ctx,
		`INSERT INTO account (oidc_subject, oidc_email, kind) VALUES ($1, $2, $3)
		 RETURNING id, oidc_subject, COALESCE(oidc_email, ''), kind, quota_bytes, active, created_at`,
		subject, email, kind).Scan(
		&u.ID, &u.OIDCSubject, &u.OIDCEmail, &u.Kind, &u.QuotaBytes, &u.Active, &u.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("account create: %w", err)
	}
	return &u, nil
}

// CreateServiceAccount creates a service account (admin only, 0007).
// No login (synthetic sub='service:<email>'), address + app passwords only.
func (s *Store) CreateServiceAccount(ctx context.Context, email string) (*store.Account, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	return s.createAccountWithAddress(ctx, "service:"+email, email, store.AccountKindService)
}

// createAccountWithAddress creates the account + primary address + INBOX in one transaction.
// ErrNotFound if the email domain is unregistered, duplicate if the address is already owned.
func (s *Store) createAccountWithAddress(ctx context.Context, subject, email, kind string) (*store.Account, error) {
	local, domainName, err := splitAddress(email)
	if err != nil {
		return nil, err
	}
	if !validLocalPart(local) {
		return nil, fmt.Errorf("invalid address: %q", email)
	}

	dom, err := s.FindDomain(ctx, domainName)
	if err != nil {
		return nil, err // domain unregistered → ErrNotFound (the login gate rejects)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var u store.Account
	err = tx.QueryRow(ctx,
		`INSERT INTO account (oidc_subject, oidc_email, kind) VALUES ($1, $2, $3)
		 RETURNING id, oidc_subject, COALESCE(oidc_email, ''), kind, quota_bytes, active, created_at`,
		subject, email, kind).Scan(
		&u.ID, &u.OIDCSubject, &u.OIDCEmail, &u.Kind, &u.QuotaBytes, &u.Active, &u.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("account create: %w", err)
	}

	// register the primary address — fails as duplicate here if another account already owns it
	if _, err := tx.Exec(ctx,
		`INSERT INTO address (domain_id, local_part, account_id) VALUES ($1, $2, $3)`,
		dom.ID, local, u.ID); err != nil {
		return nil, fmt.Errorf("address register: %w", err)
	}

	// create the default INBOX
	if _, err := tx.Exec(ctx,
		`INSERT INTO mailbox (account_id, name, uid_validity, uid_next, subscribed)
		 VALUES ($1, 'INBOX', $2, 1, true)`, u.ID, newUIDValidity()); err != nil {
		return nil, fmt.Errorf("INBOX create: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &u, nil
}

// SetAccountActive changes the user's active state.
func (s *Store) SetAccountActive(ctx context.Context, id int64, active bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE account SET active = $2 WHERE id = $1`, id, active)
	if err != nil {
		return fmt.Errorf("user state change: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── App passwords ───────────────────────────────────────────

// ListAppPassword lists the user's app passwords (including revoked, excluding hashes).
func (s *Store) ListAppPassword(ctx context.Context, accountID int64) ([]*store.AppPassword, error) {
	const q = `
		SELECT id, account_id, label, scope_list, last_used, created_at, revoked_at
		FROM app_password WHERE account_id = $1 ORDER BY created_at DESC`
	rows, err := s.pool.Query(ctx, q, accountID)
	if err != nil {
		return nil, fmt.Errorf("app password list: %w", err)
	}
	defer rows.Close()

	var out []*store.AppPassword
	for rows.Next() {
		var p store.AppPassword
		if err := rows.Scan(&p.ID, &p.AccountID, &p.Label, &p.ScopeList,
			&p.LastUsed, &p.CreatedAt, &p.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// ListAllAppPassword lists every app password (admin overview — avoids per-account fan-out).
func (s *Store) ListAllAppPassword(ctx context.Context) ([]*store.AppPassword, error) {
	const q = `
		SELECT id, account_id, label, scope_list, last_used, created_at, revoked_at
		FROM app_password ORDER BY account_id, created_at DESC`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("all app password list: %w", err)
	}
	defer rows.Close()

	var out []*store.AppPassword
	for rows.Next() {
		var p store.AppPassword
		if err := rows.Scan(&p.ID, &p.AccountID, &p.Label, &p.ScopeList,
			&p.LastUsed, &p.CreatedAt, &p.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// CreateAppPassword stores the hash (the plaintext is generated by the API layer and shown once).
func (s *Store) CreateAppPassword(ctx context.Context, accountID int64, label, hash string) (*store.AppPassword, error) {
	if strings.TrimSpace(label) == "" {
		label = "unnamed"
	}
	const q = `
		INSERT INTO app_password (account_id, label, hash) VALUES ($1, $2, $3)
		RETURNING id, account_id, label, scope_list, last_used, created_at, revoked_at`
	var p store.AppPassword
	err := s.pool.QueryRow(ctx, q, accountID, label, hash).Scan(
		&p.ID, &p.AccountID, &p.Label, &p.ScopeList, &p.LastUsed, &p.CreatedAt, &p.RevokedAt)
	if err != nil {
		return nil, fmt.Errorf("app password create: %w", err)
	}
	return &p, nil
}

// RevokeAppPassword invalidates an app password.
func (s *Store) RevokeAppPassword(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE app_password SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("app password revoke: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── Outbound queue management ───────────────────────────────

// ListOutbound lists queue items by status (raw body excluded — for listings). Empty status = all.
func (s *Store) ListOutbound(ctx context.Context, status string, limit int) ([]*store.OutboundMessage, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	const q = `
		SELECT id, envelope_from, envelope_rcpt, status, attempt_count,
		       next_attempt_at, COALESCE(last_error, ''), created_at
		FROM outbound_queue
		WHERE ($1 = '' OR status = $1)
		ORDER BY created_at DESC LIMIT $2`
	rows, err := s.pool.Query(ctx, q, status, limit)
	if err != nil {
		return nil, fmt.Errorf("queue list: %w", err)
	}
	defer rows.Close()

	var out []*store.OutboundMessage
	for rows.Next() {
		var m store.OutboundMessage
		if err := rows.Scan(&m.ID, &m.EnvelopeFrom, &m.EnvelopeRcpt, &m.Status,
			&m.AttemptCount, &m.NextAttemptAt, &m.LastError, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

// RetryOutbound flips a failed item back to waiting for immediate retry.
func (s *Store) RetryOutbound(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE outbound_queue
		 SET status = 'pending', next_attempt_at = now(), updated_at = now()
		 WHERE id = $1 AND status = 'failed'`, id)
	if err != nil {
		return fmt.Errorf("queue retry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CancelOutbound cancels a pending item. Only pending rows qualify —
// sent/failed rows are terminal, and an in-flight send (claim lease pushed
// next_attempt_at forward) still counts as pending, so cancellation wins
// only if the worker hasn't marked it yet; the raw body stays for audit.
func (s *Store) CancelOutbound(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE outbound_queue
		 SET status = 'canceled', updated_at = now()
		 WHERE id = $1 AND status = 'pending'`, id)
	if err != nil {
		return fmt.Errorf("queue cancel: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// OutboundStat returns counts per status.
func (s *Store) OutboundStat(ctx context.Context) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT status, count(*) FROM outbound_queue GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("queue stats: %w", err)
	}
	defer rows.Close()

	out := map[string]int64{}
	for rows.Next() {
		var status string
		var n int64
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[status] = n
	}
	return out, rows.Err()
}

// FindAccountByID finds an account by ID (for the admin API).
func (s *Store) FindAccountByID(ctx context.Context, id int64) (*store.Account, error) {
	const q = `
		SELECT id, oidc_subject, COALESCE(oidc_email, ''), kind,
		       quota_bytes, active, created_at
		FROM account WHERE id = $1`
	var u store.Account
	err := s.pool.QueryRow(ctx, q, id).Scan(
		&u.ID, &u.OIDCSubject, &u.OIDCEmail, &u.Kind, &u.QuotaBytes, &u.Active, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("account lookup: %w", err)
	}
	return &u, nil
}
