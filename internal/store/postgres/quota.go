package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// QuotaExceeded reports whether appending addBytes would push the account
// past its quota. NULL quota = unlimited (never exceeds). One roundtrip:
// the usage sum only runs when a quota is actually set.
func (s *Store) QuotaExceeded(ctx context.Context, accountID uuid.UUID, addBytes int64) (bool, error) {
	var exceeded bool
	err := s.pool.QueryRow(ctx, `
		SELECT CASE
			WHEN a.quota_bytes IS NULL THEN false
			ELSE COALESCE((
				SELECT sum(m.size_bytes)
				FROM message m
				JOIN mailbox mb ON mb.id = m.mailbox_id
				WHERE mb.account_id = a.id
			), 0) + $2 > a.quota_bytes
		END
		FROM account a WHERE a.id = $1`, accountID, addBytes).Scan(&exceeded)
	if err != nil {
		return false, fmt.Errorf("quota check: %w", err)
	}
	return exceeded, nil
}

// SetAccountQuota sets the account's storage quota (nil = unlimited).
func (s *Store) SetAccountQuota(ctx context.Context, id uuid.UUID, quotaBytes *int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE account SET quota_bytes = $2 WHERE id = $1`, id, quotaBytes)
	if err != nil {
		return fmt.Errorf("quota set: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListAccountUsage returns logical usage per account (sum of message sizes).
// Physical disk usage can be lower thanks to blob dedup (0002).
func (s *Store) ListAccountUsage(ctx context.Context) (map[uuid.UUID]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT mb.account_id, sum(m.size_bytes)
		FROM message m
		JOIN mailbox mb ON mb.id = m.mailbox_id
		GROUP BY mb.account_id`)
	if err != nil {
		return nil, fmt.Errorf("usage list: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]int64{}
	for rows.Next() {
		var id uuid.UUID
		var n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
