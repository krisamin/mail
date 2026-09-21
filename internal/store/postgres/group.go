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

// Permission groups (0005). The default group is the floor; groups with a
// higher position sit above it. Membership is a plain join table.

const groupSelect = `
	SELECT id, name, position, is_default,
	       can_send, can_send_external, can_receive_external,
	       daily_send_limit, created_at
	FROM account_group`

func scanGroup(row pgx.Row) (*store.AccountGroup, error) {
	var g store.AccountGroup
	err := row.Scan(&g.ID, &g.Name, &g.Position, &g.IsDefault,
		&g.CanSend, &g.CanSendExternal, &g.CanReceiveExternal,
		&g.DailySendLimit, &g.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("group lookup: %w", err)
	}
	return &g, nil
}

func validGrant(v string) bool {
	switch v {
	case store.GrantAllow, store.GrantDeny, store.GrantInherit:
		return true
	}
	return false
}

// GroupListForAccount returns the default group plus the account's own
// groups — exactly what policy.Resolve needs, in one round trip.
func (s *Store) GroupListForAccount(ctx context.Context, accountID uuid.UUID) ([]*store.AccountGroup, error) {
	const q = groupSelect + ` g
		WHERE g.is_default
		   OR EXISTS (SELECT 1 FROM account_group_member m
		              WHERE m.group_id = g.id AND m.account_id = $1)
		ORDER BY g.position DESC, g.name`
	rows, err := s.pool.Query(ctx, q, accountID)
	if err != nil {
		return nil, fmt.Errorf("group list for account: %w", err)
	}
	defer rows.Close()

	var out []*store.AccountGroup
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ListAccountGroup lists every group with its members (admin screen).
func (s *Store) ListAccountGroup(ctx context.Context) ([]*store.AccountGroup, error) {
	rows, err := s.pool.Query(ctx, groupSelect+` ORDER BY position DESC, name`)
	if err != nil {
		return nil, fmt.Errorf("group list: %w", err)
	}
	defer rows.Close()

	indexMap := map[uuid.UUID]*store.AccountGroup{}
	var out []*store.AccountGroup
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		indexMap[g.ID] = g
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	memberRows, err := s.pool.Query(ctx,
		`SELECT group_id, account_id FROM account_group_member`)
	if err != nil {
		return nil, fmt.Errorf("group member list: %w", err)
	}
	defer memberRows.Close()
	for memberRows.Next() {
		var groupID, accountID uuid.UUID
		if err := memberRows.Scan(&groupID, &accountID); err != nil {
			return nil, err
		}
		if g, ok := indexMap[groupID]; ok {
			g.MemberIDList = append(g.MemberIDList, accountID)
			g.MemberCount++
		}
	}
	return out, memberRows.Err()
}

// CreateAccountGroup adds a group above every existing one.
func (s *Store) CreateAccountGroup(ctx context.Context, name string) (*store.AccountGroup, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return nil, fmt.Errorf("invalid group name")
	}
	const q = `
		INSERT INTO account_group (name, position)
		VALUES ($1, COALESCE((SELECT MAX(position) FROM account_group), 0) + 10)
		RETURNING id, name, position, is_default,
		          can_send, can_send_external, can_receive_external,
		          daily_send_limit, created_at`
	return scanGroup(s.pool.QueryRow(ctx, q, name))
}

// UpdateAccountGroup writes a group's name and grants. The default group may
// be edited but not renamed away from its role, so its name is left alone.
func (s *Store) UpdateAccountGroup(ctx context.Context, id uuid.UUID, p store.GroupPermission) (*store.AccountGroup, error) {
	if !validGrant(p.CanSend) || !validGrant(p.CanSendExternal) || !validGrant(p.CanReceiveExternal) {
		return nil, fmt.Errorf("invalid grant")
	}
	if p.DailySendLimit != nil && *p.DailySendLimit < 0 {
		return nil, fmt.Errorf("invalid daily send limit")
	}
	name := strings.TrimSpace(p.Name)
	if name == "" || len(name) > 64 {
		return nil, fmt.Errorf("invalid group name")
	}
	const q = `
		UPDATE account_group
		SET name = CASE WHEN is_default THEN name ELSE $2 END,
		    can_send = $3, can_send_external = $4,
		    can_receive_external = $5, daily_send_limit = $6
		WHERE id = $1
		RETURNING id, name, position, is_default,
		          can_send, can_send_external, can_receive_external,
		          daily_send_limit, created_at`
	return scanGroup(s.pool.QueryRow(ctx, q, id, name,
		p.CanSend, p.CanSendExternal, p.CanReceiveExternal, p.DailySendLimit))
}

// DeleteAccountGroup removes a group. The default group stays — without a
// floor every rule would fall through to "no".
func (s *Store) DeleteAccountGroup(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM account_group WHERE id = $1 AND NOT is_default`, id)
	if err != nil {
		return fmt.Errorf("group delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("invalid delete: the default group cannot be removed")
	}
	return nil
}

// MoveAccountGroup swaps a group with its neighbour so the admin can reorder
// by clicking, without ever typing a position number. The default group is
// pinned to the bottom.
func (s *Store) MoveAccountGroup(ctx context.Context, id uuid.UUID, up bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var position int
	var isDefault bool
	err = tx.QueryRow(ctx,
		`SELECT position, is_default FROM account_group WHERE id = $1 FOR UPDATE`, id).
		Scan(&position, &isDefault)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if isDefault {
		return fmt.Errorf("invalid move: the default group stays at the bottom")
	}

	neighbour := `SELECT id, position FROM account_group
	              WHERE NOT is_default AND position > $1
	              ORDER BY position ASC LIMIT 1 FOR UPDATE`
	if !up {
		neighbour = `SELECT id, position FROM account_group
		             WHERE NOT is_default AND position < $1
		             ORDER BY position DESC LIMIT 1 FOR UPDATE`
	}
	var otherID uuid.UUID
	var otherPosition int
	err = tx.QueryRow(ctx, neighbour, position).Scan(&otherID, &otherPosition)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // already at the edge — a no-op, not an error
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE account_group SET position = $2 WHERE id = $1`, id, otherPosition); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE account_group SET position = $2 WHERE id = $1`, otherID, position); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetAccountGroupMember replaces a group's membership with the given list.
// The default group holds everybody implicitly, so it keeps no rows.
func (s *Store) SetAccountGroupMember(ctx context.Context, groupID uuid.UUID, accountIDList []uuid.UUID) error {
	var isDefault bool
	err := s.pool.QueryRow(ctx,
		`SELECT is_default FROM account_group WHERE id = $1`, groupID).Scan(&isDefault)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if isDefault {
		return fmt.Errorf("invalid membership: the default group already holds everyone")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`DELETE FROM account_group_member WHERE group_id = $1`, groupID); err != nil {
		return fmt.Errorf("group member clear: %w", err)
	}
	for _, accountID := range accountIDList {
		if _, err := tx.Exec(ctx,
			`INSERT INTO account_group_member (group_id, account_id) VALUES ($1, $2)
			 ON CONFLICT DO NOTHING`, groupID, accountID); err != nil {
			return fmt.Errorf("group member add: %w", err)
		}
	}
	return tx.Commit(ctx)
}
