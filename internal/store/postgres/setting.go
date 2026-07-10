package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/krisamin/mail/internal/store"
)

// 전역 설정 key-value (마이그레이션 0008).

// GetSetting은 설정 값을 돌려준다. 없으면 store.ErrNotFound.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.pool.QueryRow(ctx,
		`SELECT value FROM setting WHERE key = $1`, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", store.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("설정 조회(%s): %w", key, err)
	}
	return value, nil
}

// SetSetting은 설정 값을 저장한다 (upsert).
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO setting (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		key, value); err != nil {
		return fmt.Errorf("설정 저장(%s): %w", key, err)
	}
	return nil
}
