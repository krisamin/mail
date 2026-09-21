package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

)

// Permission storage (0004) — the switches themselves and the daily counter.

// SetDomainPermission writes a domain's permission switches.
func (s *Store) SetDomainPermission(ctx context.Context, id uuid.UUID, allowSend, allowReceive bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE domain SET allow_send_external = $2, allow_receive_external = $3 WHERE id = $1`,
		id, allowSend, allowReceive)
	if err != nil {
		return fmt.Errorf("domain permission update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// BumpSendCounter records n recipients against today's counter and returns the
// running total INCLUDING this send.
//
// Check and increment are one statement on purpose: two parallel sends that
// each read "99 of 100" and then wrote would both pass. Here the second one
// sees 100+n and the caller refuses it.
func (s *Store) BumpSendCounter(ctx context.Context, accountID uuid.UUID, n int) (int, error) {
	const q = `
		INSERT INTO send_counter (account_id, day, count)
		VALUES ($1, (now() AT TIME ZONE 'utc')::date, $2)
		ON CONFLICT (account_id, day)
		DO UPDATE SET count = send_counter.count + EXCLUDED.count
		RETURNING count`
	var used int
	if err := s.pool.QueryRow(ctx, q, accountID, n).Scan(&used); err != nil {
		return 0, fmt.Errorf("send counter: %w", err)
	}
	return used, nil
}

// ReleaseSendCounter gives back n slots when a send was refused after being
// counted (the limit should measure accepted mail, not rejected attempts).
func (s *Store) ReleaseSendCounter(ctx context.Context, accountID uuid.UUID, n int) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE send_counter SET count = GREATEST(count - $2, 0)
		WHERE account_id = $1 AND day = (now() AT TIME ZONE 'utc')::date`, accountID, n)
	if err != nil {
		return fmt.Errorf("send counter release: %w", err)
	}
	return nil
}

// SendCountToday reads today's counter without touching it (for the UI).
func (s *Store) SendCountToday(ctx context.Context, accountID uuid.UUID) (int, error) {
	var used int
	err := s.pool.QueryRow(ctx, `
		SELECT count FROM send_counter
		WHERE account_id = $1 AND day = (now() AT TIME ZONE 'utc')::date`, accountID).Scan(&used)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("send counter read: %w", err)
	}
	return used, nil
}

// PruneSendCounter drops rows older than keep days (called opportunistically).
func (s *Store) PruneSendCounter(ctx context.Context, keep time.Duration) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM send_counter WHERE day < ((now() AT TIME ZONE 'utc')::date - $1::int)`,
		int(keep.Hours()/24))
	return err
}
