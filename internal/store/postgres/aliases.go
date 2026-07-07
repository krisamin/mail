package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/krisamin/mail/internal/store"
)

// 별칭 — 추가 수신 주소 + 와일드카드 catch-all (마이그레이션 0004).

// ResolveAddress는 배달 대상 유저를 찾는다.
// 우선순위: 실제 유저 > 정확 별칭 > 와일드카드(*@domain).
func (s *Store) ResolveAddress(ctx context.Context, address string) (*store.User, error) {
	// 1) 실제 유저
	u, err := s.FindUserByAddress(ctx, address)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	local, domain, err := splitAddress(strings.ToLower(address))
	if err != nil {
		return nil, err
	}

	// 2) 정확 별칭 → 3) 와일드카드 (한 쿼리 — 정확 매칭 우선 정렬)
	const q = `
		SELECT u.id, u.domain_id, u.local_part, COALESCE(u.oidc_subject, ''),
		       u.quota_bytes, u.active, u.created_at
		FROM aliases a
		JOIN domains d ON d.id = a.domain_id
		JOIN users u   ON u.id = a.user_id
		WHERE d.name = $1 AND d.active AND u.active
		  AND (a.local_part = $2 OR a.local_part = '*')
		ORDER BY (a.local_part = '*') ASC
		LIMIT 1`
	var out store.User
	err = s.pool.QueryRow(ctx, q, domain, local).Scan(
		&out.ID, &out.DomainID, &out.LocalPart, &out.OIDCSubject,
		&out.QuotaBytes, &out.Active, &out.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("별칭 해석: %w", err)
	}
	return &out, nil
}

// CanSendAs는 유저가 주소로 발신 가능한지 — 본인 주소이거나 본인 별칭
// (와일드카드 별칭이면 그 도메인의 아무 local part나 허용).
func (s *Store) CanSendAs(ctx context.Context, userID int64, address string) (bool, error) {
	u, err := s.ResolveAddress(ctx, address)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return u.ID == userID, nil
}

const aliasSelect = `
	SELECT a.id, a.domain_id, a.local_part, a.user_id, a.created_at,
	       d.name, u.local_part, ud.name
	FROM aliases a
	JOIN domains d ON d.id = a.domain_id
	JOIN users u   ON u.id = a.user_id
	JOIN domains ud ON ud.id = u.domain_id`

func scanAliases(rows pgx.Rows) ([]*store.Alias, error) {
	defer rows.Close()
	var out []*store.Alias
	for rows.Next() {
		var a store.Alias
		if err := rows.Scan(&a.ID, &a.DomainID, &a.LocalPart, &a.UserID,
			&a.CreatedAt, &a.DomainName, &a.UserLocalPart, &a.UserDomainName); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

// ListAliases는 도메인의 별칭 목록.
func (s *Store) ListAliases(ctx context.Context, domainID int64) ([]*store.Alias, error) {
	rows, err := s.pool.Query(ctx, aliasSelect+` WHERE a.domain_id = $1 ORDER BY a.local_part`, domainID)
	if err != nil {
		return nil, fmt.Errorf("별칭 목록: %w", err)
	}
	return scanAliases(rows)
}

// ListUserAliases는 유저에게 걸린 별칭 목록 (도메인 무관).
func (s *Store) ListUserAliases(ctx context.Context, userID int64) ([]*store.Alias, error) {
	rows, err := s.pool.Query(ctx, aliasSelect+` WHERE a.user_id = $1 ORDER BY d.name, a.local_part`, userID)
	if err != nil {
		return nil, fmt.Errorf("유저 별칭 목록: %w", err)
	}
	return scanAliases(rows)
}

// CreateAlias는 별칭을 만든다. localPart '*'는 catch-all.
// 실제 유저 주소와 겹치면 거부 (별칭이 유저를 가리는 것 방지).
func (s *Store) CreateAlias(ctx context.Context, domainID int64, localPart string, userID int64) (*store.Alias, error) {
	localPart = strings.ToLower(strings.TrimSpace(localPart))
	if localPart == "" {
		return nil, fmt.Errorf("잘못된 별칭: local part 비어있음")
	}
	if localPart != "*" && strings.ContainsAny(localPart, "@ *") {
		return nil, fmt.Errorf("잘못된 별칭: %q ('*' 단독 또는 일반 local part만)", localPart)
	}

	// 같은 도메인의 실제 유저와 충돌 검사
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM users WHERE domain_id = $1 AND local_part = $2)`,
		domainID, localPart).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		return nil, fmt.Errorf("잘못된 별칭: %q는 실제 유저 주소", localPart)
	}

	var a store.Alias
	err := s.pool.QueryRow(ctx, `
		INSERT INTO aliases (domain_id, local_part, user_id)
		VALUES ($1, $2, $3)
		RETURNING id, domain_id, local_part, user_id, created_at`,
		domainID, localPart, userID).Scan(
		&a.ID, &a.DomainID, &a.LocalPart, &a.UserID, &a.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("별칭 생성: %w", err)
	}
	// 편의 필드 채우기
	_ = s.pool.QueryRow(ctx, `
		SELECT d.name, u.local_part, ud.name
		FROM domains d, users u
		JOIN domains ud ON ud.id = u.domain_id
		WHERE d.id = $1 AND u.id = $2`,
		a.DomainID, a.UserID).Scan(&a.DomainName, &a.UserLocalPart, &a.UserDomainName)
	return &a, nil
}

// DeleteAlias는 별칭을 지운다.
func (s *Store) DeleteAlias(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM aliases WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("별칭 삭제: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}
