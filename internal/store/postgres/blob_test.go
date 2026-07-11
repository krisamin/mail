package postgres

import (
	"context"
	"testing"
	"time"
)

// blobCount returns the number of message_blob rows.
func blobCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM message_blob`).Scan(&n); err != nil {
		t.Fatalf("blob count: %v", err)
	}
	return n
}

// TestBlobDedupAndGC: content-addressed reuse + reference-counted deletion (0002).
func TestBlobDedupAndGC(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Start clean each run (test isolation)
	_, _ = s.pool.Exec(ctx, `TRUNCATE domain, account, app_password, mailbox, message, message_flag, message_blob, outbound_queue, address, relay RESTART IDENTITY CASCADE`)

	maroID := seedAccount(t, s, "maro@krisam.in", "pw-maro-blob1")
	guestID := seedAccount(t, s, "guest@krisam.in", "pw-guest-blob1")

	maroBox, err := s.GetMailbox(ctx, maroID, "INBOX")
	if err != nil {
		t.Fatalf("maro INBOX: %v", err)
	}
	guestBox, err := s.GetMailbox(ctx, guestID, "INBOX")
	if err != nil {
		t.Fatalf("guest INBOX: %v", err)
	}

	raw := []byte("From: a@example.com\r\nSubject: fanout\r\n\r\nsame body\r\n")

	// 1) same body delivered to two accounts → one blob
	m1, err := s.AppendMessage(ctx, maroBox.ID, raw, nil, time.Now())
	if err != nil {
		t.Fatalf("append maro: %v", err)
	}
	m2, err := s.AppendMessage(ctx, guestBox.ID, raw, nil, time.Now())
	if err != nil {
		t.Fatalf("append guest: %v", err)
	}
	if m1.BlobID != m2.BlobID {
		t.Fatalf("fan-out should share one blob: %s vs %s", m1.BlobID, m2.BlobID)
	}
	if n := blobCount(t, s); n != 1 {
		t.Fatalf("blob count after fan-out should be 1: %d", n)
	}
	t.Log("✔ dedup — same body fan-out shares one blob")

	// 2) delete one reference → blob survives (still referenced)
	if err := s.SetFlag(ctx, m1.ID, []string{`\Deleted`}); err != nil {
		t.Fatalf("flag: %v", err)
	}
	if _, err := s.ExpungeDeleted(ctx, maroBox.ID, nil); err != nil {
		t.Fatalf("expunge maro: %v", err)
	}
	if n := blobCount(t, s); n != 1 {
		t.Fatalf("blob must survive while guest still references it: %d", n)
	}
	t.Log("✔ GC — referenced blob survives")

	// 3) delete the last reference → blob collected
	if err := s.SetFlag(ctx, m2.ID, []string{`\Deleted`}); err != nil {
		t.Fatalf("flag: %v", err)
	}
	if _, err := s.ExpungeDeleted(ctx, guestBox.ID, nil); err != nil {
		t.Fatalf("expunge guest: %v", err)
	}
	if n := blobCount(t, s); n != 0 {
		t.Fatalf("last reference gone — blob should be collected: %d", n)
	}
	t.Log("✔ GC — last reference removal collects the blob")

	// 4) webmail single-message delete path collects too
	m3, err := s.AppendMessage(ctx, maroBox.ID, []byte("Subject: solo\r\n\r\nonly here\r\n"), nil, time.Now())
	if err != nil {
		t.Fatalf("append solo: %v", err)
	}
	if err := s.DeleteAccountMessage(ctx, maroID, m3.ID); err != nil {
		t.Fatalf("webmail delete: %v", err)
	}
	if n := blobCount(t, s); n != 0 {
		t.Fatalf("webmail delete should collect the blob: %d", n)
	}
	t.Log("✔ GC — webmail delete path")

	// 5) mailbox deletion sweeps cascade orphans
	box2, err := s.CreateMailbox(ctx, maroID, "Work")
	if err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	if _, err := s.AppendMessage(ctx, box2.ID, []byte("Subject: in-box\r\n\r\nbye\r\n"), nil, time.Now()); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.DeleteMailbox(ctx, maroID, "Work"); err != nil {
		t.Fatalf("mailbox delete: %v", err)
	}
	if n := blobCount(t, s); n != 0 {
		t.Fatalf("mailbox delete should sweep orphaned blobs: %d", n)
	}
	t.Log("✔ GC — mailbox cascade sweep")

	// 6) copy shares the blob; deleting the copy keeps the original readable
	m4, err := s.AppendMessage(ctx, maroBox.ID, []byte("Subject: copy-src\r\n\r\ncopy me\r\n"), nil, time.Now())
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	arch, err := s.CreateMailbox(ctx, maroID, "Archive")
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	cp, err := s.CopyMessage(ctx, m4.ID, arch.ID)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if cp.BlobID != m4.BlobID {
		t.Fatal("copy should share the source blob")
	}
	if err := s.DeleteAccountMessage(ctx, maroID, cp.ID); err != nil {
		t.Fatalf("delete copy: %v", err)
	}
	if _, err := s.GetMessageBlob(ctx, m4.ID); err != nil {
		t.Fatalf("original must stay readable after copy deletion: %v", err)
	}
	t.Log("✔ GC — copy deletion keeps the shared blob")
}
