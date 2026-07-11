package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/krisamin/mail/internal/store"
)

// AppendMessage appends a message to a mailbox.
// The UID is assigned by reading and incrementing mailbox.uid_next in a transaction.
func (s *Store) AppendMessage(ctx context.Context, mailboxID uuid.UUID, raw []byte, flagList []string, internalDate time.Time) (*store.Message, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// 1) issue the UID (concurrency-safe via row lock)
	var uid int64
	err = tx.QueryRow(ctx,
		`UPDATE mailbox SET uid_next = uid_next + 1
		 WHERE id = $1 RETURNING uid_next - 1`, mailboxID).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("UID issue: %w", err)
	}

	// 2) store the blob — content-addressed by sha256 (0002). The no-op
	// DO UPDATE makes RETURNING fire on the conflict path too, so a duplicate
	// body (multi-recipient fan-out, Sent copies) reuses the existing row and
	// the row lock it takes keeps concurrent GC away until we commit.
	sum := sha256.Sum256(raw)
	var blobID uuid.UUID
	err = tx.QueryRow(ctx,
		`INSERT INTO message_blob (content, sha256) VALUES ($1, $2)
		 ON CONFLICT (sha256) DO UPDATE SET sha256 = EXCLUDED.sha256
		 RETURNING id`,
		raw, sum[:]).Scan(&blobID)
	if err != nil {
		return nil, fmt.Errorf("blob store: %w", err)
	}

	// 3) parse the header cache (best-effort — storage proceeds even on failure)
	subject, fromAddr := parseHeaderCache(raw)

	// 4) store the message metadata
	var m store.Message
	err = tx.QueryRow(ctx,
		`INSERT INTO message (mailbox_id, uid, blob_id, size_bytes, internal_date, subject, from_addr)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, mailbox_id, uid, blob_id, size_bytes, internal_date, subject, from_addr, created_at`,
		mailboxID, uid, blobID, len(raw), internalDate, subject, fromAddr).Scan(
		&m.ID, &m.MailboxID, &m.UID, &m.BlobID, &m.SizeBytes, &m.InternalDate,
		&m.Subject, &m.FromAddr, &m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("message store: %w", err)
	}

	// 5) store the flags
	for _, f := range flagList {
		if _, err := tx.Exec(ctx,
			`INSERT INTO message_flag (message_id, flag) VALUES ($1, $2)
			 ON CONFLICT DO NOTHING`, m.ID, f); err != nil {
			return nil, fmt.Errorf("flag store: %w", err)
		}
	}
	m.Flags = flagList

	// 6) change notification — published at commit time so IDLE sessions wake immediately
	if _, err := tx.Exec(ctx,
		`SELECT pg_notify('mailbox_change', $1)`,
		mailboxID.String()); err != nil {
		return nil, fmt.Errorf("change notify: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return &m, nil
}

// ListMessage returns all messages of a mailbox in UID order (flags included).
func (s *Store) ListMessage(ctx context.Context, mailboxID uuid.UUID) ([]*store.Message, error) {
	const q = `
		SELECT id, mailbox_id, uid, blob_id, size_bytes, internal_date,
		       COALESCE(subject, ''), COALESCE(from_addr, ''), created_at
		FROM message WHERE mailbox_id = $1 ORDER BY uid`
	rows, err := s.pool.Query(ctx, q, mailboxID)
	if err != nil {
		return nil, fmt.Errorf("message list: %w", err)
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

	// bulk-load the flags
	if len(messageList) > 0 {
		frows, err := s.pool.Query(ctx,
			`SELECT message_id, flag FROM message_flag WHERE message_id = ANY($1)`,
			mapKeyList(byID))
		if err != nil {
			return nil, fmt.Errorf("flag load: %w", err)
		}
		defer frows.Close()
		for frows.Next() {
			var mid uuid.UUID
			var flag string
			if err := frows.Scan(&mid, &flag); err != nil {
				return nil, err
			}
			if m := byID[mid]; m != nil {
				m.Flags = append(m.Flags, flag)
			}
		}
		if err := frows.Err(); err != nil {
			return nil, err
		}
	}
	return messageList, nil
}

// GetMessageBlob returns the raw body of a message.
func (s *Store) GetMessageBlob(ctx context.Context, messageID uuid.UUID) ([]byte, error) {
	const q = `
		SELECT b.content FROM message_blob b
		JOIN message m ON m.blob_id = b.id WHERE m.id = $1`
	var content []byte
	err := s.pool.QueryRow(ctx, q, messageID).Scan(&content)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("blob lookup: %w", err)
	}
	return content, nil
}

// SetFlag replaces the message's flags with the given set.
func (s *Store) SetFlag(ctx context.Context, messageID uuid.UUID, flagList []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM message_flag WHERE message_id = $1`, messageID); err != nil {
		return fmt.Errorf("delete existing flags: %w", err)
	}
	for _, f := range flagList {
		if _, err := tx.Exec(ctx,
			`INSERT INTO message_flag (message_id, flag) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			messageID, f); err != nil {
			return fmt.Errorf("flag insert: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// ExpungeDeleted physically deletes messages flagged \Deleted and returns the deleted UIDs.
// nil uids means the whole mailbox; otherwise only the given UIDs (IMAP UID EXPUNGE).
// Blobs that lost their last reference are garbage-collected afterwards.
func (s *Store) ExpungeDeleted(ctx context.Context, mailboxID uuid.UUID, uidSet []uint32) ([]uint32, error) {
	const q = `
		DELETE FROM message m
		WHERE m.mailbox_id = $1
		  AND ($2::bigint[] IS NULL OR m.uid = ANY($2))
		  AND EXISTS (SELECT 1 FROM message_flag f
		              WHERE f.message_id = m.id AND f.flag = '\Deleted')
		RETURNING m.uid, m.blob_id`
	var uidFilter []int64
	if uidSet != nil {
		uidFilter = make([]int64, len(uidSet))
		for i, u := range uidSet {
			uidFilter[i] = int64(u)
		}
	}
	rows, err := s.pool.Query(ctx, q, mailboxID, uidFilter)
	if err != nil {
		return nil, fmt.Errorf("expunge: %w", err)
	}
	defer rows.Close()
	var out []uint32
	var blobList []uuid.UUID
	for rows.Next() {
		var uid int64
		var blobID uuid.UUID
		if err := rows.Scan(&uid, &blobID); err != nil {
			return nil, err
		}
		out = append(out, uint32(uid))
		blobList = append(blobList, blobID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.gcBlob(ctx, blobList)
	// change notification only when something was actually deleted (reflects EXPUNGE to IDLE sessions)
	if len(out) > 0 {
		if _, err := s.pool.Exec(ctx,
			`SELECT pg_notify('mailbox_change', $1)`,
			mailboxID.String()); err != nil {
			// notification failure is not fatal — fallback polling absorbs it
			_ = err
		}
	}
	return out, nil
}

// CopyMessage copies a message to another mailbox (blob shared, metadata duplicated).
func (s *Store) CopyMessage(ctx context.Context, messageID, destMailboxID uuid.UUID) (*store.Message, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// load the source
	var src store.Message
	err = tx.QueryRow(ctx,
		`SELECT blob_id, size_bytes, internal_date, COALESCE(subject,''), COALESCE(from_addr,'')
		 FROM message WHERE id = $1`, messageID).Scan(
		&src.BlobID, &src.SizeBytes, &src.InternalDate, &src.Subject, &src.FromAddr)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("source lookup: %w", err)
	}

	// issue the dest UID
	var uid int64
	err = tx.QueryRow(ctx,
		`UPDATE mailbox SET uid_next = uid_next + 1 WHERE id = $1 RETURNING uid_next - 1`,
		destMailboxID).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("dest UID issue: %w", err)
	}

	var m store.Message
	err = tx.QueryRow(ctx,
		`INSERT INTO message (mailbox_id, uid, blob_id, size_bytes, internal_date, subject, from_addr)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, mailbox_id, uid, blob_id, size_bytes, internal_date, subject, from_addr, created_at`,
		destMailboxID, uid, src.BlobID, src.SizeBytes, src.InternalDate, src.Subject, src.FromAddr).Scan(
		&m.ID, &m.MailboxID, &m.UID, &m.BlobID, &m.SizeBytes, &m.InternalDate,
		&m.Subject, &m.FromAddr, &m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("copy insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &m, nil
}

func mapKeyList(m map[uuid.UUID]*store.Message) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
