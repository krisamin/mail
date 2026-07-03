package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/krisamin/mail/internal/store"
)

// AppendMessage는 메일박스에 메시지를 추가한다.
// UID는 mailboxes.uid_next를 트랜잭션으로 읽고 증가시켜 부여한다.
func (s *Store) AppendMessage(ctx context.Context, mailboxID int64, raw []byte, flags []string, internalDate time.Time) (*store.Message, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("트랜잭션 시작: %w", err)
	}
	defer tx.Rollback(ctx)

	// 1) UID 발급 (row lock으로 동시성 안전)
	var uid int64
	err = tx.QueryRow(ctx,
		`UPDATE mailboxes SET uid_next = uid_next + 1
		 WHERE id = $1 RETURNING uid_next - 1`, mailboxID).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("UID 발급: %w", err)
	}

	// 2) blob 저장
	sum := sha256.Sum256(raw)
	var blobID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO message_blobs (content, sha256) VALUES ($1, $2) RETURNING id`,
		raw, sum[:]).Scan(&blobID)
	if err != nil {
		return nil, fmt.Errorf("blob 저장: %w", err)
	}

	// 3) 헤더 캐시 파싱 (best-effort — 실패해도 저장은 진행)
	subject, fromAddr := parseHeaderCache(raw)

	// 4) 메시지 메타 저장
	var m store.Message
	err = tx.QueryRow(ctx,
		`INSERT INTO messages (mailbox_id, uid, blob_id, size_bytes, internal_date, subject, from_addr)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, mailbox_id, uid, blob_id, size_bytes, internal_date, subject, from_addr, created_at`,
		mailboxID, uid, blobID, len(raw), internalDate, subject, fromAddr).Scan(
		&m.ID, &m.MailboxID, &m.UID, &m.BlobID, &m.SizeBytes, &m.InternalDate,
		&m.Subject, &m.FromAddr, &m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("메시지 저장: %w", err)
	}

	// 5) 플래그 저장
	for _, f := range flags {
		if _, err := tx.Exec(ctx,
			`INSERT INTO message_flags (message_id, flag) VALUES ($1, $2)
			 ON CONFLICT DO NOTHING`, m.ID, f); err != nil {
			return nil, fmt.Errorf("플래그 저장: %w", err)
		}
	}
	m.Flags = flags

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("커밋: %w", err)
	}
	return &m, nil
}

// ListMessages는 메일박스의 모든 메시지를 UID 순으로 반환한다 (플래그 포함).
func (s *Store) ListMessages(ctx context.Context, mailboxID int64) ([]*store.Message, error) {
	const q = `
		SELECT id, mailbox_id, uid, blob_id, size_bytes, internal_date,
		       COALESCE(subject, ''), COALESCE(from_addr, ''), created_at
		FROM messages WHERE mailbox_id = $1 ORDER BY uid`
	rows, err := s.pool.Query(ctx, q, mailboxID)
	if err != nil {
		return nil, fmt.Errorf("메시지 목록: %w", err)
	}
	defer rows.Close()

	var msgs []*store.Message
	byID := map[int64]*store.Message{}
	for rows.Next() {
		var m store.Message
		if err := rows.Scan(&m.ID, &m.MailboxID, &m.UID, &m.BlobID, &m.SizeBytes,
			&m.InternalDate, &m.Subject, &m.FromAddr, &m.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, &m)
		byID[m.ID] = &m
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 플래그 일괄 로드
	if len(msgs) > 0 {
		frows, err := s.pool.Query(ctx,
			`SELECT message_id, flag FROM message_flags WHERE message_id = ANY($1)`,
			mapKeys(byID))
		if err != nil {
			return nil, fmt.Errorf("플래그 로드: %w", err)
		}
		defer frows.Close()
		for frows.Next() {
			var mid int64
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
	return msgs, nil
}

// GetMessageBlob은 메시지의 원문 본문을 반환한다.
func (s *Store) GetMessageBlob(ctx context.Context, messageID int64) ([]byte, error) {
	const q = `
		SELECT b.content FROM message_blobs b
		JOIN messages m ON m.blob_id = b.id WHERE m.id = $1`
	var content []byte
	err := s.pool.QueryRow(ctx, q, messageID).Scan(&content)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("blob 조회: %w", err)
	}
	return content, nil
}

// SetFlags는 메시지의 플래그를 지정된 집합으로 교체한다.
func (s *Store) SetFlags(ctx context.Context, messageID int64, flags []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM message_flags WHERE message_id = $1`, messageID); err != nil {
		return fmt.Errorf("기존 플래그 삭제: %w", err)
	}
	for _, f := range flags {
		if _, err := tx.Exec(ctx,
			`INSERT INTO message_flags (message_id, flag) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			messageID, f); err != nil {
			return fmt.Errorf("플래그 삽입: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// ExpungeDeleted는 \Deleted 플래그가 붙은 메시지를 실제 삭제하고, 삭제된 UID들을 반환한다.
// uids가 nil이면 메일박스 전체 대상, 아니면 해당 UID들만 (IMAP UID EXPUNGE).
func (s *Store) ExpungeDeleted(ctx context.Context, mailboxID int64, uids []uint32) ([]uint32, error) {
	const q = `
		DELETE FROM messages m
		WHERE m.mailbox_id = $1
		  AND ($2::bigint[] IS NULL OR m.uid = ANY($2))
		  AND EXISTS (SELECT 1 FROM message_flags f
		              WHERE f.message_id = m.id AND f.flag = '\Deleted')
		RETURNING m.uid`
	var uidFilter []int64
	if uids != nil {
		uidFilter = make([]int64, len(uids))
		for i, u := range uids {
			uidFilter[i] = int64(u)
		}
	}
	rows, err := s.pool.Query(ctx, q, mailboxID, uidFilter)
	if err != nil {
		return nil, fmt.Errorf("expunge: %w", err)
	}
	defer rows.Close()
	var out []uint32
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		out = append(out, uint32(uid))
	}
	return out, rows.Err()
}

// CopyMessage는 메시지를 다른 메일박스로 복사한다 (blob은 공유, 메타 복제).
func (s *Store) CopyMessage(ctx context.Context, messageID, destMailboxID int64) (*store.Message, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// 원본 로드
	var src store.Message
	err = tx.QueryRow(ctx,
		`SELECT blob_id, size_bytes, internal_date, COALESCE(subject,''), COALESCE(from_addr,'')
		 FROM messages WHERE id = $1`, messageID).Scan(
		&src.BlobID, &src.SizeBytes, &src.InternalDate, &src.Subject, &src.FromAddr)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("원본 조회: %w", err)
	}

	// dest UID 발급
	var uid int64
	err = tx.QueryRow(ctx,
		`UPDATE mailboxes SET uid_next = uid_next + 1 WHERE id = $1 RETURNING uid_next - 1`,
		destMailboxID).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("dest UID 발급: %w", err)
	}

	var m store.Message
	err = tx.QueryRow(ctx,
		`INSERT INTO messages (mailbox_id, uid, blob_id, size_bytes, internal_date, subject, from_addr)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, mailbox_id, uid, blob_id, size_bytes, internal_date, subject, from_addr, created_at`,
		destMailboxID, uid, src.BlobID, src.SizeBytes, src.InternalDate, src.Subject, src.FromAddr).Scan(
		&m.ID, &m.MailboxID, &m.UID, &m.BlobID, &m.SizeBytes, &m.InternalDate,
		&m.Subject, &m.FromAddr, &m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("복사 삽입: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &m, nil
}

func mapKeys(m map[int64]*store.Message) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
