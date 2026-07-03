package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/krisamin/mail/internal/store"
)

// EnqueueOutbound는 수신자별로 발송 항목을 큐에 넣는다 (한 트랜잭션).
func (s *Store) EnqueueOutbound(ctx context.Context, from string, rcpts []string, raw []byte) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("트랜잭션 시작: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, rcpt := range rcpts {
		if _, err := tx.Exec(ctx,
			`INSERT INTO outbound_queue (envelope_from, envelope_rcpt, raw)
			 VALUES ($1, $2, $3)`, from, rcpt, raw); err != nil {
			return fmt.Errorf("큐 삽입 (%s): %w", rcpt, err)
		}
	}
	return tx.Commit(ctx)
}

// DueOutbound는 발송 시각이 지난 pending 항목을 최대 limit개 가져온다.
// FOR UPDATE SKIP LOCKED — 여러 워커가 떠도 같은 행을 안 잡는다.
// (행 잠금은 이 호출의 트랜잭션이 끝나면 풀리므로, 워커는 가져온 뒤
// 상태를 즉시 갱신하는 게 아니라 발송 후 Mark*를 호출한다. Phase 2-3의
// 단일 워커 전제에선 충분하고, 다중 워커는 잠금 유지 트랜잭션으로 확장.)
func (s *Store) DueOutbound(ctx context.Context, limit int) ([]*store.OutboundMessage, error) {
	const q = `
		SELECT id, envelope_from, envelope_rcpt, raw, status, attempts,
		       next_attempt_at, COALESCE(last_error, ''), created_at
		FROM outbound_queue
		WHERE status = 'pending' AND next_attempt_at <= now()
		ORDER BY next_attempt_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED`
	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("due 조회: %w", err)
	}
	defer rows.Close()

	var out []*store.OutboundMessage
	for rows.Next() {
		var m store.OutboundMessage
		if err := rows.Scan(&m.ID, &m.EnvelopeFrom, &m.EnvelopeRcpt, &m.Raw,
			&m.Status, &m.Attempts, &m.NextAttemptAt, &m.LastError, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

// MarkOutboundSent는 발송 성공 처리.
func (s *Store) MarkOutboundSent(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE outbound_queue SET status = 'sent', updated_at = now() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("sent 처리: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkOutboundRetry는 실패 기록 + 다음 시도 시각 설정. attempts 증가.
func (s *Store) MarkOutboundRetry(ctx context.Context, id int64, errMsg string, nextAttempt time.Time) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE outbound_queue
		 SET attempts = attempts + 1, last_error = $2, next_attempt_at = $3, updated_at = now()
		 WHERE id = $1`, id, errMsg, nextAttempt)
	if err != nil {
		return fmt.Errorf("retry 처리: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkOutboundFailed는 영구 실패 처리 (재시도 소진 또는 5xx 영구 오류).
func (s *Store) MarkOutboundFailed(ctx context.Context, id int64, errMsg string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE outbound_queue
		 SET status = 'failed', attempts = attempts + 1, last_error = $2, updated_at = now()
		 WHERE id = $1`, id, errMsg)
	if err != nil {
		return fmt.Errorf("failed 처리: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
