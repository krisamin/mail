package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/krisamin/mail/internal/store"
)

// 주소 — 계정 소유 메일 주소 + 와일드카드 catch-all (0006).
// 기존 "계정 주소 + 별칭" 이원 구조를 address 단일 테이블로 통합.

// ResolveAddress는 배달 대상 계정을 찾는다.
// 우선순위: 정확 주소 > 와일드카드(*@domain). 한 쿼리로 정확 매칭 우선 정렬.
func (s *Store) ResolveAddress(ctx context.Context, address string) (*store.Account, error) {
	local, domain, err := splitAddress(strings.ToLower(address))
	if err != nil {
		return nil, err
	}
	const q = accountSelect + `
		JOIN address ad ON ad.account_id = a.id
		JOIN domain d ON d.id = ad.domain_id
		WHERE d.name = $1 AND d.active AND a.active
		  AND (ad.local_part = $2 OR ad.local_part = '*')
		ORDER BY (ad.local_part = '*') ASC
		LIMIT 1`
	return scanAccount(s.pool.QueryRow(ctx, q, domain, local))
}

// CanSendAs는 계정이 주소로 발신 가능한지 — 소유 주소이거나 소유한
// 와일드카드 주소의 도메인이면 그 도메인의 아무 local part나 허용.
func (s *Store) CanSendAs(ctx context.Context, accountID int64, address string) (bool, error) {
	u, err := s.ResolveAddress(ctx, address)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return u.ID == accountID, nil
}

const addressSelect = `
	SELECT ad.id, ad.domain_id, ad.local_part, ad.account_id, ad.created_at,
	       d.name, COALESCE(a.oidc_email, '')
	FROM address ad
	JOIN domain d ON d.id = ad.domain_id
	JOIN account a ON a.id = ad.account_id`

func scanAddressList(rows pgx.Rows) ([]*store.Address, error) {
	defer rows.Close()
	var out []*store.Address
	for rows.Next() {
		var ad store.Address
		if err := rows.Scan(&ad.ID, &ad.DomainID, &ad.LocalPart, &ad.AccountID,
			&ad.CreatedAt, &ad.DomainName, &ad.AccountEmail); err != nil {
			return nil, err
		}
		out = append(out, &ad)
	}
	return out, rows.Err()
}

// ListAddress는 도메인의 주소 목록.
func (s *Store) ListAddress(ctx context.Context, domainID int64) ([]*store.Address, error) {
	rows, err := s.pool.Query(ctx, addressSelect+` WHERE ad.domain_id = $1 ORDER BY ad.local_part`, domainID)
	if err != nil {
		return nil, fmt.Errorf("주소 목록: %w", err)
	}
	return scanAddressList(rows)
}

// ListAccountAddress는 계정 소유 주소 목록 (도메인 무관).
func (s *Store) ListAccountAddress(ctx context.Context, accountID int64) ([]*store.Address, error) {
	rows, err := s.pool.Query(ctx, addressSelect+` WHERE ad.account_id = $1 ORDER BY d.name, ad.local_part`, accountID)
	if err != nil {
		return nil, fmt.Errorf("계정 주소 목록: %w", err)
	}
	return scanAddressList(rows)
}

// CreateAddress는 주소를 계정에 붙인다. localPart '*'는 catch-all.
// (domain_id, local_part) UNIQUE라 이미 있는 주소면 duplicate 에러.
func (s *Store) CreateAddress(ctx context.Context, domainID int64, localPart string, accountID int64) (*store.Address, error) {
	localPart = strings.ToLower(strings.TrimSpace(localPart))
	if localPart == "" {
		return nil, fmt.Errorf("잘못된 주소: local part 비어있음")
	}
	if localPart != "*" && !validLocalPart(localPart) {
		return nil, fmt.Errorf("잘못된 주소: %q ('*' 단독 또는 일반 local part만)", localPart)
	}

	var ad store.Address
	err := s.pool.QueryRow(ctx, `
		INSERT INTO address (domain_id, local_part, account_id)
		VALUES ($1, $2, $3)
		RETURNING id, domain_id, local_part, account_id, created_at`,
		domainID, localPart, accountID).Scan(
		&ad.ID, &ad.DomainID, &ad.LocalPart, &ad.AccountID, &ad.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("주소 생성: %w", err)
	}
	// 편의 필드 채우기
	_ = s.pool.QueryRow(ctx, `
		SELECT d.name, COALESCE(a.oidc_email, '')
		FROM domain d, account a
		WHERE d.id = $1 AND a.id = $2`,
		ad.DomainID, ad.AccountID).Scan(&ad.DomainName, &ad.AccountEmail)
	return &ad, nil
}

// DeleteAddress는 주소를 지운다. 계정의 마지막 일반(비-와일드카드) 주소는
// 지울 수 없다 — 수신/로그인 매핑이 사라지는 것 방지.
func (s *Store) DeleteAddress(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM address WHERE id = $1
		  AND (local_part = '*' OR (
		    SELECT count(*) FROM address o
		    WHERE o.account_id = address.account_id AND o.local_part <> '*'
		  ) > 1)`, id)
	if err != nil {
		return fmt.Errorf("주소 삭제: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// 존재하는데 마지막 일반 주소라 못 지운 건지 구분
		var exists bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM address WHERE id = $1)`, id).Scan(&exists); err == nil && exists {
			return fmt.Errorf("잘못된 요청: 계정의 마지막 주소는 지울 수 없음")
		}
		return store.ErrNotFound
	}
	return nil
}
