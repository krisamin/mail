package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/krisamin/mail/internal/store"
)

// AdminStore 구현 (Phase 3). 컴파일 타임 체크는 store.AdminStore 쪽.
var _ store.AdminStore = (*Store)(nil)

// ── 도메인 ──────────────────────────────────────────────────

// ListDomains는 모든 도메인 (비활성 포함 — 관리 화면용).
func (s *Store) ListDomains(ctx context.Context) ([]*store.Domain, error) {
	const q = `
		SELECT id, name, active, created_at,
		       COALESCE(dkim_selector, ''), COALESCE(dkim_private_key, '')
		FROM domains ORDER BY name`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("도메인 목록: %w", err)
	}
	defer rows.Close()

	var out []*store.Domain
	for rows.Next() {
		var d store.Domain
		if err := rows.Scan(&d.ID, &d.Name, &d.Active, &d.CreatedAt,
			&d.DKIMSelector, &d.DKIMPrivateKey); err != nil {
			return nil, err
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

// CreateDomain은 새 도메인을 만든다. 소문자 정규화.
func (s *Store) CreateDomain(ctx context.Context, name string) (*store.Domain, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || !strings.Contains(name, ".") {
		return nil, fmt.Errorf("잘못된 도메인 이름: %q", name)
	}
	const q = `
		INSERT INTO domains (name) VALUES ($1)
		RETURNING id, name, active, created_at`
	var d store.Domain
	err := s.pool.QueryRow(ctx, q, name).Scan(&d.ID, &d.Name, &d.Active, &d.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("도메인 생성: %w", err)
	}
	return &d, nil
}

// SetDomainActive는 도메인 활성 상태를 바꾼다.
func (s *Store) SetDomainActive(ctx context.Context, id int64, active bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE domains SET active = $2 WHERE id = $1`, id, active)
	if err != nil {
		return fmt.Errorf("도메인 상태 변경: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetDomainDKIM은 DKIM 설정을 바꾼다. selector 빈 문자열 = 해제.
func (s *Store) SetDomainDKIM(ctx context.Context, id int64, selector, privateKeyPEM string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE domains SET dkim_selector = NULLIF($2, ''), dkim_private_key = NULLIF($3, '')
		 WHERE id = $1`, id, selector, privateKeyPEM)
	if err != nil {
		return fmt.Errorf("DKIM 설정: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── 유저 ────────────────────────────────────────────────────

// ListUsers는 도메인의 유저 목록 (비활성 포함).
func (s *Store) ListUsers(ctx context.Context, domainID int64) ([]*store.User, error) {
	const q = `
		SELECT id, domain_id, local_part, COALESCE(oidc_subject, ''),
		       quota_bytes, active, created_at
		FROM users WHERE domain_id = $1 ORDER BY local_part`
	rows, err := s.pool.Query(ctx, q, domainID)
	if err != nil {
		return nil, fmt.Errorf("유저 목록: %w", err)
	}
	defer rows.Close()

	var out []*store.User
	for rows.Next() {
		var u store.User
		if err := rows.Scan(&u.ID, &u.DomainID, &u.LocalPart, &u.OIDCSubject,
			&u.QuotaBytes, &u.Active, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &u)
	}
	return out, rows.Err()
}

// CreateUser는 새 유저를 만든다. local part 소문자 정규화 + INBOX 자동 생성.
func (s *Store) CreateUser(ctx context.Context, domainID int64, localPart string) (*store.User, error) {
	localPart = strings.ToLower(strings.TrimSpace(localPart))
	if localPart == "" || strings.ContainsAny(localPart, "@ \t") {
		return nil, fmt.Errorf("잘못된 local part: %q", localPart)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var u store.User
	err = tx.QueryRow(ctx,
		`INSERT INTO users (domain_id, local_part) VALUES ($1, $2)
		 RETURNING id, domain_id, local_part, COALESCE(oidc_subject, ''), quota_bytes, active, created_at`,
		domainID, localPart).Scan(
		&u.ID, &u.DomainID, &u.LocalPart, &u.OIDCSubject, &u.QuotaBytes, &u.Active, &u.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("유저 생성: %w", err)
	}

	// INBOX 기본 생성 (수신 경로의 자동 생성과 별개로, 처음부터 있는 게 자연스러움)
	if _, err := tx.Exec(ctx,
		`INSERT INTO mailboxes (user_id, name, uid_validity, uid_next, subscribed)
		 VALUES ($1, 'INBOX', $2, 1, true)`, u.ID, newUIDValidity()); err != nil {
		return nil, fmt.Errorf("INBOX 생성: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &u, nil
}

// SetUserActive는 유저 활성 상태를 바꾼다.
func (s *Store) SetUserActive(ctx context.Context, id int64, active bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET active = $2 WHERE id = $1`, id, active)
	if err != nil {
		return fmt.Errorf("유저 상태 변경: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── 앱 비밀번호 ─────────────────────────────────────────────

// ListAppPasswords는 유저의 앱 비밀번호 목록 (revoke 포함, 해시 제외).
func (s *Store) ListAppPasswords(ctx context.Context, userID int64) ([]*store.AppPassword, error) {
	const q = `
		SELECT id, user_id, label, scopes, last_used, created_at, revoked_at
		FROM app_passwords WHERE user_id = $1 ORDER BY created_at DESC`
	rows, err := s.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("앱비번 목록: %w", err)
	}
	defer rows.Close()

	var out []*store.AppPassword
	for rows.Next() {
		var p store.AppPassword
		if err := rows.Scan(&p.ID, &p.UserID, &p.Label, &p.Scopes,
			&p.LastUsed, &p.CreatedAt, &p.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// CreateAppPassword는 해시를 저장한다 (평문은 API 레이어가 생성·1회 노출).
func (s *Store) CreateAppPassword(ctx context.Context, userID int64, label, hash string) (*store.AppPassword, error) {
	if strings.TrimSpace(label) == "" {
		label = "unnamed"
	}
	const q = `
		INSERT INTO app_passwords (user_id, label, hash) VALUES ($1, $2, $3)
		RETURNING id, user_id, label, scopes, last_used, created_at, revoked_at`
	var p store.AppPassword
	err := s.pool.QueryRow(ctx, q, userID, label, hash).Scan(
		&p.ID, &p.UserID, &p.Label, &p.Scopes, &p.LastUsed, &p.CreatedAt, &p.RevokedAt)
	if err != nil {
		return nil, fmt.Errorf("앱비번 생성: %w", err)
	}
	return &p, nil
}

// RevokeAppPassword는 앱 비밀번호를 무효화한다.
func (s *Store) RevokeAppPassword(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE app_passwords SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("앱비번 revoke: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── 발송 큐 관리 ────────────────────────────────────────────

// ListOutbound는 상태별 큐 항목 (raw 본문 제외 — 목록용). status 빈 문자열 = 전체.
func (s *Store) ListOutbound(ctx context.Context, status string, limit int) ([]*store.OutboundMessage, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	const q = `
		SELECT id, envelope_from, envelope_rcpt, status, attempts,
		       next_attempt_at, COALESCE(last_error, ''), created_at
		FROM outbound_queue
		WHERE ($1 = '' OR status = $1)
		ORDER BY created_at DESC LIMIT $2`
	rows, err := s.pool.Query(ctx, q, status, limit)
	if err != nil {
		return nil, fmt.Errorf("큐 목록: %w", err)
	}
	defer rows.Close()

	var out []*store.OutboundMessage
	for rows.Next() {
		var m store.OutboundMessage
		if err := rows.Scan(&m.ID, &m.EnvelopeFrom, &m.EnvelopeRcpt, &m.Status,
			&m.Attempts, &m.NextAttemptAt, &m.LastError, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

// RetryOutbound는 failed 항목을 즉시 재시도 대기로 되돌린다.
func (s *Store) RetryOutbound(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE outbound_queue
		 SET status = 'pending', next_attempt_at = now(), updated_at = now()
		 WHERE id = $1 AND status = 'failed'`, id)
	if err != nil {
		return fmt.Errorf("큐 재시도: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// OutboundStats는 상태별 건수.
func (s *Store) OutboundStats(ctx context.Context) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT status, count(*) FROM outbound_queue GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("큐 통계: %w", err)
	}
	defer rows.Close()

	out := map[string]int64{}
	for rows.Next() {
		var status string
		var n int64
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[status] = n
	}
	return out, rows.Err()
}

// FindUserByID는 유저를 ID로 찾는다 (admin API용).
func (s *Store) FindUserByID(ctx context.Context, id int64) (*store.User, error) {
	const q = `
		SELECT id, domain_id, local_part, COALESCE(oidc_subject, ''),
		       quota_bytes, active, created_at
		FROM users WHERE id = $1`
	var u store.User
	err := s.pool.QueryRow(ctx, q, id).Scan(
		&u.ID, &u.DomainID, &u.LocalPart, &u.OIDCSubject, &u.QuotaBytes, &u.Active, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("유저 조회: %w", err)
	}
	return &u, nil
}
