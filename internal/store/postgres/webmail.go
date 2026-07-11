package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/krisamin/mail/internal/store"
)

// Webmail queries — every method is account-scoped by construction so the
// REST layer cannot leak another user's mail through an ID guess (IDOR-safe
// at the SQL level, not just at the handler level).

// ListMailboxSummary returns the account's mailboxes with total/unseen counts.
// INBOX sorts first, the rest alphabetically.
func (s *Store) ListMailboxSummary(ctx context.Context, accountID uuid.UUID) ([]*store.MailboxSummary, error) {
	const q = `
		SELECT mb.name,
			(SELECT count(*) FROM message m WHERE m.mailbox_id = mb.id) AS num_messages,
			(SELECT count(*) FROM message m
			   WHERE m.mailbox_id = mb.id
			     AND NOT EXISTS (SELECT 1 FROM message_flag f
			                     WHERE f.message_id = m.id AND f.flag = '\Seen')) AS num_unseen
		FROM mailbox mb
		WHERE mb.account_id = $1
		ORDER BY (mb.name <> 'INBOX'), mb.name`
	rows, err := s.pool.Query(ctx, q, accountID)
	if err != nil {
		return nil, fmt.Errorf("mailbox summary: %w", err)
	}
	defer rows.Close()

	var out []*store.MailboxSummary
	for rows.Next() {
		var m store.MailboxSummary
		var total, unseen int64
		if err := rows.Scan(&m.Name, &total, &unseen); err != nil {
			return nil, err
		}
		m.MessageCount = uint32(total)
		m.UnseenCount = uint32(unseen)
		out = append(out, &m)
	}
	return out, rows.Err()
}

// ListMessagePage returns one page of an account's mailbox, newest first.
// beforeUID=0 starts at the top; otherwise only messages with uid < beforeUID
// are returned (cursor pagination — stable under concurrent delivery, unlike
// OFFSET which shifts when new mail arrives between pages).
func (s *Store) ListMessagePage(ctx context.Context, accountID uuid.UUID, mailboxName string, limit int, beforeUID uint32) ([]*store.Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	const q = `
		SELECT m.id, m.mailbox_id, m.uid, m.blob_id, m.size_bytes, m.internal_date,
		       COALESCE(m.subject, ''), COALESCE(m.from_addr, ''), m.created_at
		FROM message m
		JOIN mailbox mb ON mb.id = m.mailbox_id
		WHERE mb.account_id = $1 AND mb.name = $2
		  AND ($3::bigint = 0 OR m.uid < $3)
		ORDER BY m.uid DESC
		LIMIT $4`
	rows, err := s.pool.Query(ctx, q, accountID, mailboxName, int64(beforeUID), limit)
	if err != nil {
		return nil, fmt.Errorf("message page: %w", err)
	}
	defer rows.Close()

	var messageList []*store.Message
	byID := map[uuid.UUID]*store.Message{}
	for rows.Next() {
		var m store.Message
		if err := rows.Scan(&m.ID, &m.MailboxID, &m.UID, &m.BlobID, &m.SizeBytes,
			&m.InternalDate, &m.Subject, &m.FromAddr, &m.CreatedAt); err != nil {
			return nil, err
		}
		messageList = append(messageList, &m)
		byID[m.ID] = &m
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.loadFlag(ctx, byID); err != nil {
		return nil, err
	}
	return messageList, nil
}

// GetAccountMessage loads one message with an ownership check baked into the
// JOIN. Returns the message plus the name of the mailbox it lives in.
func (s *Store) GetAccountMessage(ctx context.Context, accountID, messageID uuid.UUID) (*store.Message, string, error) {
	const q = `
		SELECT m.id, m.mailbox_id, m.uid, m.blob_id, m.size_bytes, m.internal_date,
		       COALESCE(m.subject, ''), COALESCE(m.from_addr, ''), m.created_at, mb.name
		FROM message m
		JOIN mailbox mb ON mb.id = m.mailbox_id
		WHERE mb.account_id = $1 AND m.id = $2`
	var m store.Message
	var mailboxName string
	err := s.pool.QueryRow(ctx, q, accountID, messageID).Scan(
		&m.ID, &m.MailboxID, &m.UID, &m.BlobID, &m.SizeBytes,
		&m.InternalDate, &m.Subject, &m.FromAddr, &m.CreatedAt, &mailboxName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("message lookup: %w", err)
	}
	if err := s.loadFlag(ctx, map[uuid.UUID]*store.Message{m.ID: &m}); err != nil {
		return nil, "", err
	}
	return &m, mailboxName, nil
}

// MoveAccountMessage moves a message into another of the account's mailboxes,
// creating the destination on demand (Trash/Archive appear on first use).
// The message gets a fresh UID in the destination (IMAP UIDs are
// mailbox-scoped and never reused); both mailboxes are notified so IDLE
// sessions see the disappearance/appearance immediately.
func (s *Store) MoveAccountMessage(ctx context.Context, accountID, messageID uuid.UUID, destName string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// ownership check + source mailbox
	var srcMailboxID uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT m.mailbox_id FROM message m
		JOIN mailbox mb ON mb.id = m.mailbox_id
		WHERE mb.account_id = $1 AND m.id = $2`, accountID, messageID).Scan(&srcMailboxID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("move source lookup: %w", err)
	}

	// destination (create on demand)
	var destMailboxID uuid.UUID
	err = tx.QueryRow(ctx,
		`SELECT id FROM mailbox WHERE account_id = $1 AND name = $2`,
		accountID, destName).Scan(&destMailboxID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `
			INSERT INTO mailbox (account_id, name, uid_validity, uid_next, subscribed)
			VALUES ($1, $2, $3, 1, true) RETURNING id`,
			accountID, destName, newUIDValidity()).Scan(&destMailboxID)
	}
	if err != nil {
		return fmt.Errorf("move destination: %w", err)
	}
	if destMailboxID == srcMailboxID {
		return tx.Commit(ctx) // no-op move
	}

	// fresh UID in the destination
	var uid int64
	if err := tx.QueryRow(ctx,
		`UPDATE mailbox SET uid_next = uid_next + 1 WHERE id = $1 RETURNING uid_next - 1`,
		destMailboxID).Scan(&uid); err != nil {
		return fmt.Errorf("move UID issue: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE message SET mailbox_id = $2, uid = $3 WHERE id = $1`,
		messageID, destMailboxID, uid); err != nil {
		return fmt.Errorf("move update: %w", err)
	}

	for _, id := range []uuid.UUID{srcMailboxID, destMailboxID} {
		if _, err := tx.Exec(ctx, `SELECT pg_notify('mailbox_change', $1)`,
			id.String()); err != nil {
			return fmt.Errorf("move notify: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// DeleteAccountMessage physically deletes one message (ownership-checked).
// The webmail layer uses this only for messages already in Trash — everything
// else is moved to Trash first (two-step delete like every mail client).
func (s *Store) DeleteAccountMessage(ctx context.Context, accountID, messageID uuid.UUID) error {
	var mailboxID, blobID uuid.UUID
	err := s.pool.QueryRow(ctx, `
		DELETE FROM message m
		USING mailbox mb
		WHERE mb.id = m.mailbox_id AND mb.account_id = $1 AND m.id = $2
		RETURNING m.mailbox_id, m.blob_id`, accountID, messageID).Scan(&mailboxID, &blobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("message delete: %w", err)
	}
	s.gcBlob(ctx, []uuid.UUID{blobID})
	if _, err := s.pool.Exec(ctx, `SELECT pg_notify('mailbox_change', $1)`,
		mailboxID.String()); err != nil {
		// notification failure is not fatal — fallback polling absorbs it
		_ = err
	}
	return nil
}

// SetAccountMessageFlag replaces the flags of an account-owned message and
// notifies the mailbox (flag changes show up in IMAP IDLE too).
func (s *Store) SetAccountMessageFlag(ctx context.Context, accountID, messageID uuid.UUID, flagList []string) error {
	m, _, err := s.GetAccountMessage(ctx, accountID, messageID)
	if err != nil {
		return err
	}
	if err := s.SetFlag(ctx, messageID, flagList); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `SELECT pg_notify('mailbox_change', $1)`,
		m.MailboxID.String()); err != nil {
		_ = err
	}
	return nil
}

// EnsureMailbox finds or creates a mailbox by name (webmail Sent/Trash flows).
func (s *Store) EnsureMailbox(ctx context.Context, accountID uuid.UUID, name string) (*store.Mailbox, error) {
	box, err := s.GetMailbox(ctx, accountID, name)
	if errors.Is(err, store.ErrNotFound) {
		box, err = s.CreateMailbox(ctx, accountID, name)
	}
	return box, err
}

// loadFlag bulk-loads flags for the given messages (shared by list/detail).
func (s *Store) loadFlag(ctx context.Context, byID map[uuid.UUID]*store.Message) error {
	if len(byID) == 0 {
		return nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT message_id, flag FROM message_flag WHERE message_id = ANY($1)`,
		mapKeyList(byID))
	if err != nil {
		return fmt.Errorf("flag load: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var mid uuid.UUID
		var flag string
		if err := rows.Scan(&mid, &flag); err != nil {
			return err
		}
		if m := byID[mid]; m != nil {
			m.Flags = append(m.Flags, flag)
		}
	}
	return rows.Err()
}
