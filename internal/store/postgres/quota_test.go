package postgres

import (
	"context"
	"testing"
	"time"
)

// TestQuotaEnforcement: NULL = unlimited, set quota blocks, freeing unblocks.
func TestQuotaEnforcement(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	_, _ = s.pool.Exec(ctx, `TRUNCATE domain, account, app_password, mailbox, message, message_flag, message_blob, outbound_queue, address, relay RESTART IDENTITY CASCADE`)

	accountID := seedAccount(t, s, "maro@krisam.in", "pw-quota-test")
	box, err := s.GetMailbox(ctx, accountID, "INBOX")
	if err != nil {
		t.Fatalf("INBOX: %v", err)
	}

	// unlimited (NULL quota) never exceeds
	over, err := s.QuotaExceeded(ctx, accountID, 1<<40)
	if err != nil || over {
		t.Fatalf("NULL quota must never exceed: over=%v err=%v", over, err)
	}
	t.Log("✔ NULL quota = unlimited")

	// set a 1KB quota, store ~600B → next 600B append would exceed
	quota := int64(1024)
	if err := s.SetAccountQuota(ctx, accountID, &quota); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	raw := make([]byte, 600)
	copy(raw, []byte("Subject: q\r\n\r\n"))
	if _, err := s.AppendMessage(ctx, box.ID, raw, nil, time.Now()); err != nil {
		t.Fatalf("append: %v", err)
	}
	over, err = s.QuotaExceeded(ctx, accountID, 600)
	if err != nil || !over {
		t.Fatalf("600+600 > 1024 must exceed: over=%v err=%v", over, err)
	}
	over, err = s.QuotaExceeded(ctx, accountID, 100)
	if err != nil || over {
		t.Fatalf("600+100 <= 1024 must fit: over=%v err=%v", over, err)
	}
	t.Log("✔ quota boundary math")

	// clearing the quota (nil) unblocks
	if err := s.SetAccountQuota(ctx, accountID, nil); err != nil {
		t.Fatalf("clear quota: %v", err)
	}
	over, err = s.QuotaExceeded(ctx, accountID, 1<<40)
	if err != nil || over {
		t.Fatalf("cleared quota must never exceed: over=%v err=%v", over, err)
	}
	t.Log("✔ quota clear = unlimited again")

	// usage listing reflects the stored message
	usage, err := s.ListAccountUsage(ctx)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage[accountID] != 600 {
		t.Fatalf("usage should be 600: %d", usage[accountID])
	}
	t.Log("✔ usage listing")
}
