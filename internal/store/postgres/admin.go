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

// ListDomain는 모든 도메인 (비활성 포함 — 관리 화면용).
func (s *Store) ListDomain(ctx context.Context) ([]*store.Domain, error) {
	const q = `
		SELECT id, name, active, created_at,
		       COALESCE(dkim_selector, ''), COALESCE(dkim_private_key, ''), relay_id
		FROM domain ORDER BY name`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("도메인 목록: %w", err)
	}
	defer rows.Close()

	var out []*store.Domain
	for rows.Next() {
		var d store.Domain
		if err := rows.Scan(&d.ID, &d.Name, &d.Active, &d.CreatedAt,
			&d.DKIMSelector, &d.DKIMPrivateKey, &d.RelayID); err != nil {
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
		INSERT INTO domain (name) VALUES ($1)
		RETURNING id, name, active, created_at`
	var d store.Domain
	err := s.pool.QueryRow(ctx, q, name).Scan(&d.ID, &d.Name, &d.Active, &d.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("도메인 생성: %w", err)
	}
	return &d, nil
}

// BackfillDomainAddress는 oidc_email이 이 도메인인 기존 사람 계정들에게
// primary 주소 + INBOX를 소급 생성한다 (도메인 추가 직후 호출).
// 이미 주소가 있거나(다른 계정 소유 포함) 서비스 계정은 건너뛴다. 멱등.
// 생성된 주소 수를 돌려준다.
func (s *Store) BackfillDomainAddress(ctx context.Context, domainID int64) (int, error) {
	// 도메인 확인
	var domainName string
	err := s.pool.QueryRow(ctx,
		`SELECT name FROM domain WHERE id = $1`, domainID).Scan(&domainName)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("도메인 조회: %w", err)
	}

	// 대상: oidc_email이 @<domain>인 활성 사람 계정, 아직 그 주소가 없는 경우
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, lower(split_part(a.oidc_email, '@', 1))
		FROM account a
		WHERE a.kind = 'user' AND a.active
		  AND lower(split_part(a.oidc_email, '@', 2)) = $1
		  AND NOT EXISTS (
		    SELECT 1 FROM address x
		    WHERE x.domain_id = $2
		      AND x.local_part = lower(split_part(a.oidc_email, '@', 1)))
		ORDER BY a.id`, domainName, domainID)
	if err != nil {
		return 0, fmt.Errorf("backfill 대상 조회: %w", err)
	}
	type target struct {
		accountID int64
		local     string
	}
	var targetList []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.accountID, &t.local); err != nil {
			rows.Close()
			return 0, err
		}
		targetList = append(targetList, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	created := 0
	for _, t := range targetList {
		if t.local == "" || t.local == "*" {
			continue
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return created, err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO address (domain_id, local_part, account_id) VALUES ($1, $2, $3)`,
			domainID, t.local, t.accountID); err != nil {
			tx.Rollback(ctx)
			return created, fmt.Errorf("backfill 주소 생성(%s@%s): %w", t.local, domainName, err)
		}
		// INBOX 없으면 생성 (bare 계정은 INBOX도 없다)
		if _, err := tx.Exec(ctx,
			`INSERT INTO mailbox (account_id, name, uid_validity, uid_next, subscribed)
			 VALUES ($1, 'INBOX', $2, 1, true)
			 ON CONFLICT (account_id, name) DO NOTHING`,
			t.accountID, newUIDValidity()); err != nil {
			tx.Rollback(ctx)
			return created, fmt.Errorf("backfill INBOX 생성: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

// SetDomainActive는 도메인 활성 상태를 바꾼다.
func (s *Store) SetDomainActive(ctx context.Context, id int64, active bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE domain SET active = $2 WHERE id = $1`, id, active)
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
		`UPDATE domain SET dkim_selector = NULLIF($2, ''), dkim_private_key = NULLIF($3, '')
		 WHERE id = $1`, id, selector, privateKeyPEM)
	if err != nil {
		return fmt.Errorf("DKIM 설정: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── 계정 ────────────────────────────────────────────────────

// ListAccount는 전체 계정 목록 (비활성 포함 — 관리 화면용).
func (s *Store) ListAccount(ctx context.Context) ([]*store.Account, error) {
	const q = `
		SELECT id, oidc_subject, COALESCE(oidc_email, ''), kind,
		       quota_bytes, active, created_at
		FROM account ORDER BY kind, oidc_email`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("계정 목록: %w", err)
	}
	defer rows.Close()

	var out []*store.Account
	for rows.Next() {
		var u store.Account
		if err := rows.Scan(&u.ID, &u.OIDCSubject, &u.OIDCEmail, &u.Kind,
			&u.QuotaBytes, &u.Active, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &u)
	}
	return out, rows.Err()
}

// ProvisionAccount는 OIDC sub 기준 JIT 프로비저닝 (멱등).
//   - sub로 기존 계정 찾으면: oidc_email 갱신 후 반환 (주소는 안 건드림 —
//     IdP에서 email이 바뀌어도 기존 주소는 admin이 관리)
//   - 없으면: 계정 생성. email 도메인이 등록돼 있으면 primary 주소 +
//     INBOX까지, 미등록이면 계정만 (주소 없음 = 메일 사용 불가.
//     나중에 admin이 도메인을 추가하면 backfill로 주소가 생긴다).
//   - email 주소가 이미 다른 계정 소유면 duplicate 에러 (충돌 —
//     admin이 정리해야 하는 상태).
func (s *Store) ProvisionAccount(ctx context.Context, subject, email string) (*store.Account, error) {
	subject = strings.TrimSpace(subject)
	email = strings.ToLower(strings.TrimSpace(email))
	if subject == "" {
		return nil, fmt.Errorf("잘못된 신원: sub 비어있음")
	}

	// 기존 계정 — email만 갱신 (비활성 계정은 로그인 차단이 목적이라 제외)
	if u, err := s.FindAccountBySubject(ctx, subject); err == nil {
		if u.OIDCEmail != email {
			if _, err := s.pool.Exec(ctx,
				`UPDATE account SET oidc_email = $2 WHERE id = $1`, u.ID, email); err != nil {
				return nil, fmt.Errorf("계정 email 갱신: %w", err)
			}
			u.OIDCEmail = email
		}
		return u, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	// 입양: 같은 email을 primary로 가진 기존 사람 계정(시드/마이그레이션의
	// placeholder sub, 또는 IdP에서 유저 재생성으로 sub가 바뀐 경우)이
	// 있으면 sub를 갱신해 이어받는다. ★서비스 계정은 입양 불가 — IdP에
	// 같은 email 유저를 만들어도 서비스 계정을 탈취할 수 없다.
	// 주소가 존재하되 소유 계정의 oidc_email이 다르면(남의 추가 주소)도
	// 입양 금지 — 아래 INSERT에서 duplicate로 실패한다.
	if owner, err := s.FindAccountByAddress(ctx, email); err == nil &&
		owner.OIDCEmail == email && owner.Kind == store.AccountKindUser {
		if _, err := s.pool.Exec(ctx,
			`UPDATE account SET oidc_subject = $2 WHERE id = $1`, owner.ID, subject); err != nil {
			return nil, fmt.Errorf("계정 신원 갱신: %w", err)
		}
		owner.OIDCSubject = subject
		return owner, nil
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	u, err := s.createAccountWithAddress(ctx, subject, email, store.AccountKindUser)
	if errors.Is(err, store.ErrNotFound) {
		// 도메인 미등록 — 계정만 만든다 (주소/INBOX 없음 = 메일 사용 불가).
		// admin이 나중에 도메인을 추가하면 BackfillDomainAddress가 채운다.
		return s.createBareAccount(ctx, subject, email, store.AccountKindUser)
	}
	return u, err
}

// createBareAccount는 주소 없는 계정만 만든다 (도메인 미등록 유저의 로그인).
func (s *Store) createBareAccount(ctx context.Context, subject, email, kind string) (*store.Account, error) {
	var u store.Account
	err := s.pool.QueryRow(ctx,
		`INSERT INTO account (oidc_subject, oidc_email, kind) VALUES ($1, $2, $3)
		 RETURNING id, oidc_subject, COALESCE(oidc_email, ''), kind, quota_bytes, active, created_at`,
		subject, email, kind).Scan(
		&u.ID, &u.OIDCSubject, &u.OIDCEmail, &u.Kind, &u.QuotaBytes, &u.Active, &u.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("계정 생성: %w", err)
	}
	return &u, nil
}

// CreateServiceAccount는 서비스 계정을 만든다 (admin 전용, 0007).
// 로그인 불가(sub='service:<email>' 합성값), 주소+앱비밀번호만.
func (s *Store) CreateServiceAccount(ctx context.Context, email string) (*store.Account, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	return s.createAccountWithAddress(ctx, "service:"+email, email, store.AccountKindService)
}

// createAccountWithAddress는 계정 + primary 주소 + INBOX를 한 트랜잭션으로 만든다.
// email 도메인 미등록이면 ErrNotFound, 주소가 이미 소유돼 있으면 duplicate.
func (s *Store) createAccountWithAddress(ctx context.Context, subject, email, kind string) (*store.Account, error) {
	local, domainName, err := splitAddress(email)
	if err != nil {
		return nil, err
	}
	if local == "" || local == "*" || strings.ContainsAny(local, "@ 	") {
		return nil, fmt.Errorf("잘못된 주소: %q", email)
	}

	dom, err := s.FindDomain(ctx, domainName)
	if err != nil {
		return nil, err // 도메인 미등록 → ErrNotFound (로그인 게이트가 거부)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var u store.Account
	err = tx.QueryRow(ctx,
		`INSERT INTO account (oidc_subject, oidc_email, kind) VALUES ($1, $2, $3)
		 RETURNING id, oidc_subject, COALESCE(oidc_email, ''), kind, quota_bytes, active, created_at`,
		subject, email, kind).Scan(
		&u.ID, &u.OIDCSubject, &u.OIDCEmail, &u.Kind, &u.QuotaBytes, &u.Active, &u.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("계정 생성: %w", err)
	}

	// primary 주소 등록 — 이미 다른 계정 소유면 여기서 duplicate로 실패
	if _, err := tx.Exec(ctx,
		`INSERT INTO address (domain_id, local_part, account_id) VALUES ($1, $2, $3)`,
		dom.ID, local, u.ID); err != nil {
		return nil, fmt.Errorf("주소 등록: %w", err)
	}

	// INBOX 기본 생성
	if _, err := tx.Exec(ctx,
		`INSERT INTO mailbox (account_id, name, uid_validity, uid_next, subscribed)
		 VALUES ($1, 'INBOX', $2, 1, true)`, u.ID, newUIDValidity()); err != nil {
		return nil, fmt.Errorf("INBOX 생성: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &u, nil
}

// SetAccountActive는 유저 활성 상태를 바꾼다.
func (s *Store) SetAccountActive(ctx context.Context, id int64, active bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE account SET active = $2 WHERE id = $1`, id, active)
	if err != nil {
		return fmt.Errorf("유저 상태 변경: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── 앱 비밀번호 ─────────────────────────────────────────────

// ListAppPassword는 유저의 앱 비밀번호 목록 (revoke 포함, 해시 제외).
func (s *Store) ListAppPassword(ctx context.Context, accountID int64) ([]*store.AppPassword, error) {
	const q = `
		SELECT id, account_id, label, scope_list, last_used, created_at, revoked_at
		FROM app_password WHERE account_id = $1 ORDER BY created_at DESC`
	rows, err := s.pool.Query(ctx, q, accountID)
	if err != nil {
		return nil, fmt.Errorf("앱비번 목록: %w", err)
	}
	defer rows.Close()

	var out []*store.AppPassword
	for rows.Next() {
		var p store.AppPassword
		if err := rows.Scan(&p.ID, &p.AccountID, &p.Label, &p.ScopeList,
			&p.LastUsed, &p.CreatedAt, &p.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// CreateAppPassword는 해시를 저장한다 (평문은 API 레이어가 생성·1회 노출).
func (s *Store) CreateAppPassword(ctx context.Context, accountID int64, label, hash string) (*store.AppPassword, error) {
	if strings.TrimSpace(label) == "" {
		label = "unnamed"
	}
	const q = `
		INSERT INTO app_password (account_id, label, hash) VALUES ($1, $2, $3)
		RETURNING id, account_id, label, scope_list, last_used, created_at, revoked_at`
	var p store.AppPassword
	err := s.pool.QueryRow(ctx, q, accountID, label, hash).Scan(
		&p.ID, &p.AccountID, &p.Label, &p.ScopeList, &p.LastUsed, &p.CreatedAt, &p.RevokedAt)
	if err != nil {
		return nil, fmt.Errorf("앱비번 생성: %w", err)
	}
	return &p, nil
}

// RevokeAppPassword는 앱 비밀번호를 무효화한다.
func (s *Store) RevokeAppPassword(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE app_password SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
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
		SELECT id, envelope_from, envelope_rcpt, status, attempt_count,
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
			&m.AttemptCount, &m.NextAttemptAt, &m.LastError, &m.CreatedAt); err != nil {
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

// OutboundStat는 상태별 건수.
func (s *Store) OutboundStat(ctx context.Context) (map[string]int64, error) {
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

// FindAccountByID는 계정을 ID로 찾는다 (admin API용).
func (s *Store) FindAccountByID(ctx context.Context, id int64) (*store.Account, error) {
	const q = `
		SELECT id, oidc_subject, COALESCE(oidc_email, ''), kind,
		       quota_bytes, active, created_at
		FROM account WHERE id = $1`
	var u store.Account
	err := s.pool.QueryRow(ctx, q, id).Scan(
		&u.ID, &u.OIDCSubject, &u.OIDCEmail, &u.Kind, &u.QuotaBytes, &u.Active, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("계정 조회: %w", err)
	}
	return &u, nil
}
