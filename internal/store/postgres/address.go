package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/krisamin/mail/internal/store"
)

// Addresses — account-owned mail addresses + wildcard catch-all (0006).
// The former dual "account address + alias" structure is merged into a single address table.

// ResolveAddress finds the account to deliver to.
// Priority: exact address > wildcard (*@domain). One query, sorted with exact matches first.
func (s *Store) ResolveAddress(ctx context.Context, address string) (*store.Account, error) {
	local, domain, err := splitAddress(strings.ToLower(address))
	if err != nil {
		return nil, err
	}
	const q = accountSelect + `
		JOIN address ad ON ad.account_id = a.id
		JOIN domain d ON d.id = ad.domain_id
		WHERE d.name = $1 AND d.active AND a.active
		  AND (ad.local_part = $2 OR ad.local_part = '*')
		ORDER BY (ad.local_part = '*') ASC
		LIMIT 1`
	return scanAccount(s.pool.QueryRow(ctx, q, domain, local))
}

// CanSendAs reports whether the account may send as the address — either an
// owned address, or, when the account owns the domain's wildcard address,
// any local part of that domain is allowed.
func (s *Store) CanSendAs(ctx context.Context, accountID uuid.UUID, address string) (bool, error) {
	u, err := s.ResolveAddress(ctx, address)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return u.ID == accountID, nil
}

const addressSelect = `
	SELECT ad.id, ad.domain_id, ad.local_part, ad.account_id, ad.created_at,
	       d.name, COALESCE(a.oidc_email, '')
	FROM address ad
	JOIN domain d ON d.id = ad.domain_id
	JOIN account a ON a.id = ad.account_id`

func scanAddressList(rows pgx.Rows) ([]*store.Address, error) {
	defer rows.Close()
	var out []*store.Address
	for rows.Next() {
		var ad store.Address
		if err := rows.Scan(&ad.ID, &ad.DomainID, &ad.LocalPart, &ad.AccountID,
			&ad.CreatedAt, &ad.DomainName, &ad.AccountEmail); err != nil {
			return nil, err
		}
		out = append(out, &ad)
	}
	return out, rows.Err()
}

// ListAddress lists the addresses of a domain.
func (s *Store) ListAddress(ctx context.Context, domainID uuid.UUID) ([]*store.Address, error) {
	rows, err := s.pool.Query(ctx, addressSelect+` WHERE ad.domain_id = $1 ORDER BY ad.local_part`, domainID)
	if err != nil {
		return nil, fmt.Errorf("address list: %w", err)
	}
	return scanAddressList(rows)
}

// ListAccountAddress lists the addresses owned by an account (across domains).
func (s *Store) ListAccountAddress(ctx context.Context, accountID uuid.UUID) ([]*store.Address, error) {
	rows, err := s.pool.Query(ctx, addressSelect+` WHERE ad.account_id = $1 ORDER BY d.name, ad.local_part`, accountID)
	if err != nil {
		return nil, fmt.Errorf("account address list: %w", err)
	}
	return scanAddressList(rows)
}

// ListAllAddress lists every address (admin overview — avoids per-account fan-out).
func (s *Store) ListAllAddress(ctx context.Context) ([]*store.Address, error) {
	rows, err := s.pool.Query(ctx, addressSelect+` ORDER BY ad.account_id, d.name, ad.local_part`)
	if err != nil {
		return nil, fmt.Errorf("all address list: %w", err)
	}
	return scanAddressList(rows)
}

// CreateAddress attaches an address to an account. localPart '*' is the catch-all.
// (domain_id, local_part) is UNIQUE, so an existing address yields a duplicate error.
func (s *Store) CreateAddress(ctx context.Context, domainID uuid.UUID, localPart string, accountID uuid.UUID) (*store.Address, error) {
	localPart = strings.ToLower(strings.TrimSpace(localPart))
	if localPart == "" {
		return nil, fmt.Errorf("invalid address: empty local part")
	}
	if localPart != "*" && !validLocalPart(localPart) {
		return nil, fmt.Errorf("invalid address: %q (only standalone '*' or a regular local part)", localPart)
	}

	var ad store.Address
	err := s.pool.QueryRow(ctx, `
		INSERT INTO address (domain_id, local_part, account_id)
		VALUES ($1, $2, $3)
		RETURNING id, domain_id, local_part, account_id, created_at`,
		domainID, localPart, accountID).Scan(
		&ad.ID, &ad.DomainID, &ad.LocalPart, &ad.AccountID, &ad.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("address create: %w", err)
	}
	// fill convenience fields
	_ = s.pool.QueryRow(ctx, `
		SELECT d.name, COALESCE(a.oidc_email, '')
		FROM domain d, account a
		WHERE d.id = $1 AND a.id = $2`,
		ad.DomainID, ad.AccountID).Scan(&ad.DomainName, &ad.AccountEmail)
	return &ad, nil
}

// DeleteAddress deletes an address. The account's last regular (non-wildcard)
// address cannot be deleted — prevents losing the receive/login mapping.
func (s *Store) DeleteAddress(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM address WHERE id = $1
		  AND (local_part = '*' OR (
		    SELECT count(*) FROM address o
		    WHERE o.account_id = address.account_id AND o.local_part <> '*'
		  ) > 1)`, id)
	if err != nil {
		return fmt.Errorf("address delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// distinguish: exists but undeletable because it's the last regular address
		var exists bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM address WHERE id = $1)`, id).Scan(&exists); err == nil && exists {
			return fmt.Errorf("invalid request: cannot delete the account's last address")
		}
		return store.ErrNotFound
	}
	return nil
}
