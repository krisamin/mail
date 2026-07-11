package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/krisamin/mail/internal/store"
)

// Filter rule storage (0009). All access is account-scoped — a rule id from
// another account is a plain ErrNotFound.

const filterRuleColumnList = `
	id, account_id, position, name, active,
	field, header_name, match_type, pattern,
	action, action_mailbox, created_at`

func scanFilterRule(row pgx.Row) (*store.FilterRule, error) {
	var r store.FilterRule
	err := row.Scan(&r.ID, &r.AccountID, &r.Position, &r.Name, &r.Active,
		&r.Field, &r.HeaderName, &r.MatchType, &r.Pattern,
		&r.Action, &r.ActionMailbox, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("filter rule scan: %w", err)
	}
	return &r, nil
}

func (s *Store) listFilterRule(ctx context.Context, accountID uuid.UUID, activeOnly bool) ([]*store.FilterRule, error) {
	q := `SELECT ` + filterRuleColumnList + ` FROM filter_rule WHERE account_id = $1`
	if activeOnly {
		q += ` AND active`
	}
	q += ` ORDER BY position, id`
	rows, err := s.pool.Query(ctx, q, accountID)
	if err != nil {
		return nil, fmt.Errorf("filter rule list: %w", err)
	}
	defer rows.Close()
	var out []*store.FilterRule
	for rows.Next() {
		r, err := scanFilterRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListActiveFilterRule is the delivery-path read (active only, position order).
func (s *Store) ListActiveFilterRule(ctx context.Context, accountID uuid.UUID) ([]*store.FilterRule, error) {
	return s.listFilterRule(ctx, accountID, true)
}

// ListFilterRule returns every rule of the account (management UI).
func (s *Store) ListFilterRule(ctx context.Context, accountID uuid.UUID) ([]*store.FilterRule, error) {
	return s.listFilterRule(ctx, accountID, false)
}

// CreateFilterRule appends a rule at the end of the account's list.
func (s *Store) CreateFilterRule(ctx context.Context, r *store.FilterRule) (*store.FilterRule, error) {
	if err := validateFilterRule(r); err != nil {
		return nil, err
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO filter_rule
			(account_id, position, name, active, field, header_name, match_type, pattern, action, action_mailbox)
		VALUES ($1,
			(SELECT COALESCE(MAX(position), 0) + 1 FROM filter_rule WHERE account_id = $1),
			$2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+filterRuleColumnList,
		r.AccountID, r.Name, r.Active, r.Field, r.HeaderName, r.MatchType,
		r.Pattern, r.Action, r.ActionMailbox)
	return scanFilterRule(row)
}

// UpdateFilterRule rewrites the mutable fields (position untouched — use SwapFilterRule).
func (s *Store) UpdateFilterRule(ctx context.Context, r *store.FilterRule) error {
	if err := validateFilterRule(r); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE filter_rule
		SET name = $3, active = $4, field = $5, header_name = $6,
		    match_type = $7, pattern = $8, action = $9, action_mailbox = $10
		WHERE account_id = $1 AND id = $2`,
		r.AccountID, r.ID, r.Name, r.Active, r.Field, r.HeaderName,
		r.MatchType, r.Pattern, r.Action, r.ActionMailbox)
	if err != nil {
		return fmt.Errorf("filter rule update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteFilterRule removes a rule (account-scoped).
func (s *Store) DeleteFilterRule(ctx context.Context, accountID, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM filter_rule WHERE account_id = $1 AND id = $2`, accountID, id)
	if err != nil {
		return fmt.Errorf("filter rule delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SwapFilterRule swaps positions with the neighbor above (-1) or below (+1).
// No-op when the rule is already at the edge.
func (s *Store) SwapFilterRule(ctx context.Context, accountID, id uuid.UUID, direction int) error {
	if direction != -1 && direction != 1 {
		return fmt.Errorf("invalid direction (must be -1 or 1)")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var pos int
	err = tx.QueryRow(ctx,
		`SELECT position FROM filter_rule WHERE account_id = $1 AND id = $2 FOR UPDATE`,
		accountID, id).Scan(&pos)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("filter rule position: %w", err)
	}

	// neighbor in the requested direction
	var neighborID uuid.UUID
	var neighborPos int
	order := "ASC"
	cmp := ">"
	if direction == -1 {
		order = "DESC"
		cmp = "<"
	}
	err = tx.QueryRow(ctx, fmt.Sprintf(`
		SELECT id, position FROM filter_rule
		WHERE account_id = $1 AND position %s $2
		ORDER BY position %s, id LIMIT 1 FOR UPDATE`, cmp, order),
		accountID, pos).Scan(&neighborID, &neighborPos)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // already at the edge — no-op
	}
	if err != nil {
		return fmt.Errorf("filter rule neighbor: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE filter_rule SET position = $2 WHERE id = $1`, id, neighborPos); err != nil {
		return fmt.Errorf("filter rule swap: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE filter_rule SET position = $2 WHERE id = $1`, neighborID, pos); err != nil {
		return fmt.Errorf("filter rule swap: %w", err)
	}
	return tx.Commit(ctx)
}

// validateFilterRule guards the enum-ish columns — the values come from the
// API layer, but the store is the last line before SQL.
func validateFilterRule(r *store.FilterRule) error {
	switch r.Field {
	case store.FilterFieldFrom, store.FilterFieldTo, store.FilterFieldSubject:
	case store.FilterFieldHeader:
		if r.HeaderName == "" {
			return fmt.Errorf("invalid filter rule: header field requires headerName")
		}
	default:
		return fmt.Errorf("invalid filter rule field %q", r.Field)
	}
	switch r.MatchType {
	case store.FilterMatchContains, store.FilterMatchEquals,
		store.FilterMatchPrefix, store.FilterMatchSuffix:
	default:
		return fmt.Errorf("invalid filter rule matchType %q", r.MatchType)
	}
	switch r.Action {
	case store.FilterActionMarkSeen, store.FilterActionFlag, store.FilterActionDiscard:
	case store.FilterActionMove:
		if r.ActionMailbox == "" {
			return fmt.Errorf("invalid filter rule: move action requires mailbox")
		}
	default:
		return fmt.Errorf("invalid filter rule action %q", r.Action)
	}
	if r.Pattern == "" {
		return fmt.Errorf("invalid filter rule: pattern required")
	}
	if r.Name == "" {
		return fmt.Errorf("invalid filter rule: name required")
	}
	return nil
}
