package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/krisamin/mail/internal/store"
)

// 발송 relay (마이그레이션 0005) — env 하드코딩 대신 DB로 여러 relay 관리.

const relayColumnList = `id, name, host, port, username, password, starttls, is_default, active, created_at`

func scanRelay(row pgx.Row) (*store.Relay, error) {
	var r store.Relay
	err := row.Scan(&r.ID, &r.Name, &r.Host, &r.Port, &r.Username, &r.Password,
		&r.StartTLS, &r.IsDefault, &r.Active, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ResolveRelay는 발신 도메인의 relay를 찾는다.
// 도메인 지정(domain.relay_id) → default → ErrNotFound (호출자가 env fallback).
func (s *Store) ResolveRelay(ctx context.Context, senderDomain string) (*store.Relay, error) {
	senderDomain = strings.ToLower(strings.TrimSpace(senderDomain))

	// 한 쿼리: 도메인 지정 relay 우선, 없으면 default.
	q := `
		SELECT ` + relayColumnList + ` FROM relay r
		WHERE r.active AND (
			r.id = (SELECT relay_id FROM domain WHERE name = $1)
			OR r.is_default
		)
		ORDER BY (r.id = (SELECT relay_id FROM domain WHERE name = $1)) DESC
		LIMIT 1`
	r, err := scanRelay(s.pool.QueryRow(ctx, q, senderDomain))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("relay 해석: %w", err)
	}
	return r, nil
}

// ListRelay는 모든 relay (관리 화면용 — password 포함이니 API에서 마스킹할 것).
func (s *Store) ListRelay(ctx context.Context) ([]*store.Relay, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+relayColumnList+` FROM relay ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("relay 목록: %w", err)
	}
	defer rows.Close()
	var out []*store.Relay
	for rows.Next() {
		var r store.Relay
		if err := rows.Scan(&r.ID, &r.Name, &r.Host, &r.Port, &r.Username, &r.Password,
			&r.StartTLS, &r.IsDefault, &r.Active, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

func validateRelay(r *store.Relay) error {
	r.Name = strings.ToLower(strings.TrimSpace(r.Name))
	r.Host = strings.TrimSpace(r.Host)
	if r.Name == "" || r.Host == "" {
		return fmt.Errorf("잘못된 relay: name/host 필수")
	}
	if r.Port <= 0 || r.Port > 65535 {
		return fmt.Errorf("잘못된 relay: port %d", r.Port)
	}
	return nil
}

// CreateRelay는 relay를 만든다. is_default=true면 기존 default를 해제한다.
func (s *Store) CreateRelay(ctx context.Context, r *store.Relay) (*store.Relay, error) {
	if err := validateRelay(r); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if r.IsDefault {
		if _, err := tx.Exec(ctx, `UPDATE relay SET is_default = false WHERE is_default`); err != nil {
			return nil, fmt.Errorf("default 해제: %w", err)
		}
	}
	out, err := scanRelay(tx.QueryRow(ctx, `
		INSERT INTO relay (name, host, port, username, password, starttls, is_default, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+relayColumnList,
		r.Name, r.Host, r.Port, r.Username, r.Password, r.StartTLS, r.IsDefault, r.Active))
	if err != nil {
		return nil, fmt.Errorf("relay 생성: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateRelay는 relay를 수정한다. Password 빈 문자열 = 기존 값 유지.
func (s *Store) UpdateRelay(ctx context.Context, r *store.Relay) (*store.Relay, error) {
	if err := validateRelay(r); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if r.IsDefault {
		if _, err := tx.Exec(ctx, `UPDATE relay SET is_default = false WHERE is_default AND id <> $1`, r.ID); err != nil {
			return nil, fmt.Errorf("default 해제: %w", err)
		}
	}
	out, err := scanRelay(tx.QueryRow(ctx, `
		UPDATE relay SET name = $2, host = $3, port = $4, username = $5,
		       password = COALESCE(NULLIF($6, ''), password),
		       starttls = $7, is_default = $8, active = $9
		WHERE id = $1
		RETURNING `+relayColumnList,
		r.ID, r.Name, r.Host, r.Port, r.Username, r.Password, r.StartTLS, r.IsDefault, r.Active))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("relay 수정: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteRelay는 relay를 지운다. 도메인의 relay_id는 FK ON DELETE SET NULL.
func (s *Store) DeleteRelay(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM relay WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("relay 삭제: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// SetDomainRelay는 도메인 발신 relay를 지정한다 (nil = default 사용).
func (s *Store) SetDomainRelay(ctx context.Context, domainID int64, relayID *int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE domain SET relay_id = $2 WHERE id = $1`, domainID, relayID)
	if err != nil {
		return fmt.Errorf("도메인 relay 지정: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}
