package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/krisamin/mail/internal/store"
)

// EnqueueOutbound enqueues an outbound item per recipient (one transaction).
func (s *Store) EnqueueOutbound(ctx context.Context, from string, rcptList []string, raw []byte) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, rcpt := range rcptList {
		if _, err := tx.Exec(ctx,
			`INSERT INTO outbound_queue (envelope_from, envelope_rcpt, raw)
			 VALUES ($1, $2, $3)`, from, rcpt, raw); err != nil {
			return fmt.Errorf("queue insert (%s): %w", rcpt, err)
		}
	}
	return tx.Commit(ctx)
}

// DueOutbound fetches up to limit pending items whose send time has passed.
// FOR UPDATE SKIP LOCKED — multiple workers never grab the same row.
//
// ★Claim lease: fetched rows get next_attempt_at pushed forward by
// claimLease in the same statement. If the worker crashes mid-send or
// MarkOutboundSent fails, the row is NOT immediately re-eligible — it only
// comes back after the lease expires. This bounds the duplicate-send window
// (at-least-once stays, but a marking hiccup no longer causes an instant
// duplicate on the next 10s poll).
func (s *Store) DueOutbound(ctx context.Context, limit int) ([]*store.OutboundMessage, error) {
	const claimLease = 5 * time.Minute
	const q = `
		UPDATE outbound_queue SET next_attempt_at = now() + $2::interval
		WHERE id IN (
			SELECT id FROM outbound_queue
			WHERE status = 'pending' AND next_attempt_at <= now()
			ORDER BY next_attempt_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, envelope_from, envelope_rcpt, raw, status, attempt_count,
		          next_attempt_at, COALESCE(last_error, ''), created_at`
	rows, err := s.pool.Query(ctx, q, limit, claimLease.String())
	if err != nil {
		return nil, fmt.Errorf("due lookup: %w", err)
	}
	defer rows.Close()

	var out []*store.OutboundMessage
	for rows.Next() {
		var m store.OutboundMessage
		if err := rows.Scan(&m.ID, &m.EnvelopeFrom, &m.EnvelopeRcpt, &m.Raw,
			&m.Status, &m.AttemptCount, &m.NextAttemptAt, &m.LastError, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

// MarkOutboundSent marks delivery success.
func (s *Store) MarkOutboundSent(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE outbound_queue SET status = 'sent', updated_at = now() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("mark sent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkOutboundRetry records the failure + sets the next attempt time. attemptCount is incremented.
func (s *Store) MarkOutboundRetry(ctx context.Context, id int64, errMsg string, nextAttempt time.Time) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE outbound_queue
		 SET attempt_count = attempt_count + 1, last_error = $2, next_attempt_at = $3, updated_at = now()
		 WHERE id = $1`, id, errMsg, nextAttempt)
	if err != nil {
		return fmt.Errorf("mark retry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkOutboundFailed marks a permanent failure (retries exhausted or a permanent 5xx error).
func (s *Store) MarkOutboundFailed(ctx context.Context, id int64, errMsg string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE outbound_queue
		 SET status = 'failed', attempt_count = attempt_count + 1, last_error = $2, updated_at = now()
		 WHERE id = $1`, id, errMsg)
	if err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
