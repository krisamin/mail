package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/krisamin/mail/internal/store"
)

// newUIDValidity는 새 메일박스의 UIDVALIDITY를 생성한다.
// 유닉스 초를 쓴다 (단조 증가, 재생성 시 달라짐 보장).
func newUIDValidity() uint32 {
	return uint32(time.Now().Unix())
}

// ListMailbox는 유저의 모든 메일박스를 반환한다.
func (s *Store) ListMailbox(ctx context.Context, accountID int64) ([]*store.Mailbox, error) {
	const q = `
		SELECT id, account_id, name, uid_validity, uid_next, subscribed, created_at
		FROM mailbox WHERE account_id = $1 ORDER BY name`
	rows, err := s.pool.Query(ctx, q, accountID)
	if err != nil {
		return nil, fmt.Errorf("메일박스 목록: %w", err)
	}
	defer rows.Close()

	var out []*store.Mailbox
	for rows.Next() {
		var m store.Mailbox
		if err := rows.Scan(&m.ID, &m.AccountID, &m.Name, &m.UIDValidity, &m.UIDNext, &m.Subscribed, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

// GetMailbox는 이름으로 메일박스를 찾는다.
func (s *Store) GetMailbox(ctx context.Context, accountID int64, name string) (*store.Mailbox, error) {
	const q = `
		SELECT id, account_id, name, uid_validity, uid_next, subscribed, created_at
		FROM mailbox WHERE account_id = $1 AND name = $2`
	var m store.Mailbox
	err := s.pool.QueryRow(ctx, q, accountID, name).Scan(
		&m.ID, &m.AccountID, &m.Name, &m.UIDValidity, &m.UIDNext, &m.Subscribed, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("메일박스 조회: %w", err)
	}
	return &m, nil
}

// CreateMailbox는 새 메일박스를 만든다.
func (s *Store) CreateMailbox(ctx context.Context, accountID int64, name string) (*store.Mailbox, error) {
	const q = `
		INSERT INTO mailbox (account_id, name, uid_validity, uid_next, subscribed)
		VALUES ($1, $2, $3, 1, true)
		RETURNING id, account_id, name, uid_validity, uid_next, subscribed, created_at`
	var m store.Mailbox
	err := s.pool.QueryRow(ctx, q, accountID, name, newUIDValidity()).Scan(
		&m.ID, &m.AccountID, &m.Name, &m.UIDValidity, &m.UIDNext, &m.Subscribed, &m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("메일박스 생성: %w", err)
	}
	return &m, nil
}

// DeleteMailbox는 메일박스를 삭제한다 (메시지도 CASCADE).
func (s *Store) DeleteMailbox(ctx context.Context, accountID int64, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM mailbox WHERE account_id = $1 AND name = $2`, accountID, name)
	if err != nil {
		return fmt.Errorf("메일박스 삭제: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RenameMailbox는 메일박스 이름을 바꾼다.
func (s *Store) RenameMailbox(ctx context.Context, accountID int64, name, newName string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE mailbox SET name = $3 WHERE account_id = $1 AND name = $2`, accountID, name, newName)
	if err != nil {
		return fmt.Errorf("메일박스 이름변경: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSubscribed는 구독 상태를 바꾼다.
func (s *Store) SetSubscribed(ctx context.Context, mailboxID int64, subscribed bool) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE mailbox SET subscribed = $2 WHERE id = $1`, mailboxID, subscribed)
	return err
}

// MailboxStatus는 SELECT/STATUS용 집계값을 계산한다.
func (s *Store) MailboxStatus(ctx context.Context, mailboxID int64) (*store.MailboxStatus, error) {
	const q = `
		SELECT
			mb.uid_next,
			mb.uid_validity,
			(SELECT count(*) FROM message m WHERE m.mailbox_id = mb.id) AS num_messages,
			(SELECT count(*) FROM message m
			   WHERE m.mailbox_id = mb.id
			     AND NOT EXISTS (SELECT 1 FROM message_flag f
			                     WHERE f.message_id = m.id AND f.flag = '\Seen')) AS num_unseen
		FROM mailbox mb WHERE mb.id = $1`
	var st store.MailboxStatus
	var uidNext, uidValidity int64
	var numMessages, numUnseen int64
	err := s.pool.QueryRow(ctx, q, mailboxID).Scan(&uidNext, &uidValidity, &numMessages, &numUnseen)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("메일박스 상태: %w", err)
	}
	st.UIDNext = uint32(uidNext)
	st.UIDValidity = uint32(uidValidity)
	st.MessageCount = uint32(numMessages)
	st.UnseenCount = uint32(numUnseen)
	st.NumRecent = 0 // RECENT는 obsolete, 0으로
	return &st, nil
}
