package postgres

import (
	"context"
	"testing"
	"time"
)

// Greylist store integration tests (uses the dev DB like the rest).

func TestCheckGreylist(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, _ = s.pool.Exec(ctx, `TRUNCATE greylist`)

	const (
		net1 = "1.2.3.0/24"
		from = "sender@example.com"
		rcpt = "maro@krisam.in"
	)

	// 1) first contact — blocked
	pass, err := s.CheckGreylist(ctx, net1, from, rcpt, time.Minute, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if pass {
		t.Fatal("first contact must be greylisted")
	}
	// immediate retry — still blocked (delay not elapsed)
	pass, err = s.CheckGreylist(ctx, net1, from, rcpt, time.Minute, 30*24*time.Hour)
	if err != nil || pass {
		t.Fatalf("retry before delay must stay greylisted: %v %v", pass, err)
	}
	t.Log("✔ first contact + early retry blocked")

	// 2) with zero delay the same triplet passes immediately (delay elapsed)
	pass, err = s.CheckGreylist(ctx, net1, from, rcpt, 0, 30*24*time.Hour)
	if err != nil || !pass {
		t.Fatalf("elapsed delay must pass: %v %v", pass, err)
	}
	t.Log("✔ post-delay retry passes")

	// 3) different rcpt — a separate triplet, blocked again
	pass, err = s.CheckGreylist(ctx, net1, from, "other@krisam.in", time.Minute, 30*24*time.Hour)
	if err != nil || pass {
		t.Fatalf("new triplet must be greylisted: %v %v", pass, err)
	}
	t.Log("✔ triplet isolation")

	// 4) stale reset — backdate last_seen beyond staleAfter, probation restarts
	if _, err := s.pool.Exec(ctx, `
		UPDATE greylist SET last_seen = now() - interval '40 days',
		                    first_seen = now() - interval '40 days'
		WHERE source_net = $1 AND envelope_from = $2 AND envelope_rcpt = $3`,
		net1, from, rcpt); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	pass, err = s.CheckGreylist(ctx, net1, from, rcpt, time.Minute, 30*24*time.Hour)
	if err != nil || pass {
		t.Fatalf("stale triplet must re-enter probation: %v %v", pass, err)
	}
	t.Log("✔ stale triplet resets probation")
}
