package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/krisamin/mail/internal/store"
)

// Global settings key-value (migration 0008).

// GetSetting returns a setting value. store.ErrNotFound when missing.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.pool.QueryRow(ctx,
		`SELECT value FROM setting WHERE key = $1`, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", store.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("setting lookup(%s): %w", key, err)
	}
	return value, nil
}

// SetSetting stores a setting value (upsert).
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO setting (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		key, value); err != nil {
		return fmt.Errorf("setting store(%s): %w", key, err)
	}
	return nil
}
