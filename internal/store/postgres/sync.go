package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/krisamin/mail/internal/store"
)

// Sync queries for the internal API (ara). Same rule as webmail.go: the
// account scope is part of every statement (JOIN mailbox ... account_id), so
// a foreign mailbox or message id can never leak through the handler layer.

// ListMessageSince returns the messages of an account mailbox with
// uid > sinceUID in ascending UID order — the sync cursor. Flags included.
func (s *Store) ListMessageSince(ctx context.Context, accountID uuid.UUID, mailboxName string, sinceUID uint32, limit int) ([]*store.Message, error) {
	const q = `
		SELECT m.id, m.mailbox_id, m.uid, m.blob_id, m.size_bytes, m.internal_date,
		       COALESCE(m.subject, ''), COALESCE(m.from_addr, ''), m.created_at,
		       m.preview, m.to_addr, m.sync_meta
		FROM message m
		JOIN mailbox mb ON mb.id = m.mailbox_id
		WHERE mb.account_id = $1 AND mb.name = $2 AND m.uid > $3
		ORDER BY m.uid
		LIMIT $4`
	rows, err := s.pool.Query(ctx, q, accountID, mailboxName, int64(sinceUID), limit)
	if err != nil {
		return nil, fmt.Errorf("message since: %w", err)
	}
	defer rows.Close()

	var messageList []*store.Message
	byID := map[uuid.UUID]*store.Message{}
	for rows.Next() {
		var m store.Message
		if err := rows.Scan(&m.ID, &m.MailboxID, &m.UID, &m.BlobID, &m.SizeBytes,
			&m.InternalDate, &m.Subject, &m.FromAddr, &m.CreatedAt,
			&m.Preview, &m.ToAddr, &m.SyncMeta); err != nil {
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

// SetMessageSyncMeta stores the parsed sync cache (and fills preview/to_addr
// if they were never computed). Ownership is part of the statement, so a
// foreign message id is a silent no-op.
func (s *Store) SetMessageSyncMeta(ctx context.Context, accountID, messageID uuid.UUID, meta []byte, preview, toAddr string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE message m
		SET sync_meta = $3::text::jsonb,
		    preview = COALESCE(m.preview, $4),
		    to_addr = COALESCE(m.to_addr, $5)
		FROM mailbox mb
		WHERE mb.id = m.mailbox_id AND mb.account_id = $1 AND m.id = $2`,
		accountID, messageID, string(meta), preview, toAddr)
	if err != nil {
		return fmt.Errorf("message sync cache: %w", err)
	}
	return nil
}

// ListFlagSince returns (uid, flags) for every message of an account mailbox
// with uid >= fromUID, ascending, at most limit rows. A UID missing from a
// full listing is how a consumer learns about expunges.
func (s *Store) ListFlagSince(ctx context.Context, accountID uuid.UUID, mailboxName string, fromUID uint32, limit int) ([]store.UIDFlag, error) {
	const q = `
		SELECT m.uid,
		       COALESCE(array_agg(f.flag ORDER BY f.flag) FILTER (WHERE f.flag IS NOT NULL), '{}')
		FROM message m
		JOIN mailbox mb ON mb.id = m.mailbox_id
		LEFT JOIN message_flag f ON f.message_id = m.id
		WHERE mb.account_id = $1 AND mb.name = $2 AND m.uid >= $3
		GROUP BY m.uid
		ORDER BY m.uid
		LIMIT $4`
	rows, err := s.pool.Query(ctx, q, accountID, mailboxName, int64(fromUID), limit)
	if err != nil {
		return nil, fmt.Errorf("flag since: %w", err)
	}
	defer rows.Close()
	out := []store.UIDFlag{}
	for rows.Next() {
		var uid int64
		var flagList []string
		if err := rows.Scan(&uid, &flagList); err != nil {
			return nil, err
		}
		out = append(out, store.UIDFlag{UID: uint32(uid), FlagList: flagList})
	}
	return out, rows.Err()
}
