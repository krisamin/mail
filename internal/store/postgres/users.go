package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/argon2"

	"github.com/krisamin/mail/internal/store"
)

// ErrNotFound는 조회 대상이 없을 때 (store.ErrNotFound 별칭 — 하위호환).
var ErrNotFound = store.ErrNotFound

// ErrAuthFailed는 인증 실패 (store.ErrAuthFailed 별칭 — 하위호환).
var ErrAuthFailed = store.ErrAuthFailed

// splitAddress는 'maro@krisam.in' → ('maro', 'krisam.in').
func splitAddress(address string) (local, domain string, err error) {
	at := strings.LastIndex(address, "@")
	if at < 0 {
		return "", "", fmt.Errorf("잘못된 주소: %q", address)
	}
	return address[:at], address[at+1:], nil
}

// FindDomain은 활성 도메인을 이름으로 찾는다.
func (s *Store) FindDomain(ctx context.Context, name string) (*store.Domain, error) {
	const q = `
		SELECT id, name, active, created_at,
		       COALESCE(dkim_selector, ''), COALESCE(dkim_private_key, '')
		FROM domains WHERE name = $1 AND active`
	var d store.Domain
	err := s.pool.QueryRow(ctx, q, name).Scan(
		&d.ID, &d.Name, &d.Active, &d.CreatedAt, &d.DKIMSelector, &d.DKIMPrivateKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("도메인 조회: %w", err)
	}
	return &d, nil
}

// FindUserByAddress는 이메일 주소로 활성 유저를 찾는다.
func (s *Store) FindUserByAddress(ctx context.Context, address string) (*store.User, error) {
	local, domain, err := splitAddress(address)
	if err != nil {
		return nil, err
	}
	const q = `
		SELECT u.id, u.domain_id, u.local_part, COALESCE(u.oidc_subject, ''),
		       u.quota_bytes, u.active, u.created_at
		FROM users u
		JOIN domains d ON d.id = u.domain_id
		WHERE u.local_part = $1 AND d.name = $2 AND u.active AND d.active`
	var u store.User
	err = s.pool.QueryRow(ctx, q, local, domain).Scan(
		&u.ID, &u.DomainID, &u.LocalPart, &u.OIDCSubject,
		&u.QuotaBytes, &u.Active, &u.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("유저 조회: %w", err)
	}
	return &u, nil
}

// AuthenticateAppPassword는 주소+앱비밀번호로 인증한다.
// 해당 유저의 revoke 안 된 앱 비밀번호들과 argon2id 비교.
func (s *Store) AuthenticateAppPassword(ctx context.Context, address, password string) (*store.User, error) {
	u, err := s.FindUserByAddress(ctx, address)
	if err != nil {
		return nil, err
	}
	const q = `
		SELECT id, hash FROM app_passwords
		WHERE user_id = $1 AND revoked_at IS NULL`
	rows, err := s.pool.Query(ctx, q, u.ID)
	if err != nil {
		return nil, fmt.Errorf("앱 비밀번호 조회: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var hash string
		if err := rows.Scan(&id, &hash); err != nil {
			return nil, err
		}
		if verifyPassword(password, hash) {
			// last_used 갱신 (best-effort)
			_, _ = s.pool.Exec(ctx, `UPDATE app_passwords SET last_used = now() WHERE id = $1`, id)
			return u, nil
		}
	}
	return nil, ErrAuthFailed
}

// ── argon2id 헬퍼 ───────────────────────────────────────────
// 포맷: argon2id$<time>$<memoryKiB>$<threads>$<saltB64>$<hashB64>
// (스파이크용 최소 구현. Phase 2에서 파라미터/포맷 재검토)

const (
	argonTime    = 1
	argonMemory  = 64 * 1024 // 64MB
	argonThreads = 4
	argonKeyLen  = 32
)

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "argon2id" {
		return false
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return subtleConstEq(got, want)
}
