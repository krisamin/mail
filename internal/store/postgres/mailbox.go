package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/krisamin/mail/internal/store"
)

// newUIDValidity generates the UIDVALIDITY for a new mailbox.
// Uses unix seconds (monotonically increasing, guaranteed to differ on recreation).
func newUIDValidity() uint32 {
	return uint32(time.Now().Unix())
}

// ListMailbox returns all mailboxes of a user.
func (s *Store) ListMailbox(ctx context.Context, accountID uuid.UUID) ([]*store.Mailbox, error) {
	const q = `
		SELECT id, account_id, name, uid_validity, uid_next, subscribed, created_at
		FROM mailbox WHERE account_id = $1 ORDER BY name`
	rows, err := s.pool.Query(ctx, q, accountID)
	if err != nil {
		return nil, fmt.Errorf("mailbox list: %w", err)
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

// GetMailbox finds a mailbox by name.
func (s *Store) GetMailbox(ctx context.Context, accountID uuid.UUID, name string) (*store.Mailbox, error) {
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
		return nil, fmt.Errorf("mailbox lookup: %w", err)
	}
	return &m, nil
}

// CreateMailbox creates a new mailbox.
func (s *Store) CreateMailbox(ctx context.Context, accountID uuid.UUID, name string) (*store.Mailbox, error) {
	const q = `
		INSERT INTO mailbox (account_id, name, uid_validity, uid_next, subscribed)
		VALUES ($1, $2, $3, 1, true)
		RETURNING id, account_id, name, uid_validity, uid_next, subscribed, created_at`
	var m store.Mailbox
	err := s.pool.QueryRow(ctx, q, accountID, name, newUIDValidity()).Scan(
		&m.ID, &m.AccountID, &m.Name, &m.UIDValidity, &m.UIDNext, &m.Subscribed, &m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("mailbox create: %w", err)
	}
	return &m, nil
}

// DeleteMailbox deletes a mailbox (messages CASCADE too).
func (s *Store) DeleteMailbox(ctx context.Context, accountID uuid.UUID, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM mailbox WHERE account_id = $1 AND name = $2`, accountID, name)
	if err != nil {
		return fmt.Errorf("mailbox delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RenameMailbox renames a mailbox.
func (s *Store) RenameMailbox(ctx context.Context, accountID uuid.UUID, name, newName string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE mailbox SET name = $3 WHERE account_id = $1 AND name = $2`, accountID, name, newName)
	if err != nil {
		return fmt.Errorf("mailbox rename: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSubscribed changes the subscription state.
func (s *Store) SetSubscribed(ctx context.Context, mailboxID uuid.UUID, subscribed bool) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE mailbox SET subscribed = $2 WHERE id = $1`, mailboxID, subscribed)
	return err
}

// MailboxStatus computes the aggregates for SELECT/STATUS.
func (s *Store) MailboxStatus(ctx context.Context, mailboxID uuid.UUID) (*store.MailboxStatus, error) {
	const q = `
		SELECT
			mb.uid_next,
			mb.uid_validity,
			(SELECT count(*) FROM message m WHERE m.mailbox_id = mb.id) AS num_messages,
			(SELECT count(*) FROM message m
			   WHERE m.mailbox_id = mb.id
			     AND NOT EXISTS (SELECT 1 FROM message_flag f
			                     WHERE f.message_id = m.id AND f.flag = '\Seen')) AS num_unseen,
			(SELECT count(*) FROM message m
			   WHERE m.mailbox_id = mb.id
			     AND EXISTS (SELECT 1 FROM message_flag f
			                 WHERE f.message_id = m.id AND f.flag = '\Deleted')) AS num_deleted,
			(SELECT COALESCE(sum(m.size_bytes), 0) FROM message m
			   WHERE m.mailbox_id = mb.id) AS total_bytes
		FROM mailbox mb WHERE mb.id = $1`
	var st store.MailboxStatus
	var uidNext, uidValidity int64
	var numMessages, numUnseen, numDeleted, totalBytes int64
	err := s.pool.QueryRow(ctx, q, mailboxID).Scan(&uidNext, &uidValidity, &numMessages, &numUnseen, &numDeleted, &totalBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mailbox status: %w", err)
	}
	st.UIDNext = uint32(uidNext)
	st.UIDValidity = uint32(uidValidity)
	st.MessageCount = uint32(numMessages)
	st.UnseenCount = uint32(numUnseen)
	st.DeletedCount = uint32(numDeleted)
	st.TotalBytes = totalBytes
	st.NumRecent = 0 // RECENT is obsolete, keep 0
	return &st, nil
}
