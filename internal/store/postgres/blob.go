package postgres

import (
	"context"
	"log"

	"github.com/google/uuid"
)

// gcBlob deletes the given blobs if no message references them anymore
// (content-addressed dedup, 0002). Called inline after delete paths with the
// blob ids the deleted messages pointed at — candidate-based, so there is no
// full-table sweep and no cron.
//
// Concurrency note: AppendMessage takes a row lock on the blob (no-op
// ON CONFLICT DO UPDATE) before committing a new reference, so this DELETE
// blocks until that transaction finishes and then sees the new reference.
// The NOT EXISTS re-check therefore never removes a blob that just gained
// a reader. Best-effort: a failure only leaves an orphan behind, which a
// later delete of the same content picks up again.
func (s *Store) gcBlob(ctx context.Context, blobList []uuid.UUID) {
	if len(blobList) == 0 {
		return
	}
	// dedupe candidates (fan-out deletes repeat the same blob id)
	seen := make(map[uuid.UUID]bool, len(blobList))
	unique := blobList[:0]
	for _, id := range blobList {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM message_blob b
		WHERE b.id = ANY($1)
		  AND NOT EXISTS (SELECT 1 FROM message m WHERE m.blob_id = b.id)`,
		unique); err != nil {
		log.Printf("store: blob GC failed (%d candidates, orphans remain): %v", len(unique), err)
	}
}

// gcMailboxBlob garbage-collects after a whole-mailbox delete: the messages
// are already gone via ON DELETE CASCADE, so candidates cannot be collected
// from RETURNING — instead sweep blobs that lost every reference. Still
// bounded: only rows with no referencing message qualify, and the reverse
// index (idx_message_blob_ref) makes the existence probe cheap.
func (s *Store) gcMailboxBlob(ctx context.Context) {
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM message_blob b
		WHERE NOT EXISTS (SELECT 1 FROM message m WHERE m.blob_id = b.id)`); err != nil {
		log.Printf("store: mailbox blob GC failed (orphans remain): %v", err)
	}
}
