package postgres

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"
)

// Greylisting triplet store (0010).
//
// One statement handles the whole state machine: INSERT the triplet, or on
// conflict refresh last_seen — and reset first_seen when the row went stale
// (an old triplet must re-earn trust). The pass decision falls out of the
// returned first_seen/pass_count.

// CheckGreylist implements store.Store.
func (s *Store) CheckGreylist(ctx context.Context, sourceNet, from, rcpt string, minDelay, staleAfter time.Duration) (bool, error) {
	const q = `
		INSERT INTO greylist (source_net, envelope_from, envelope_rcpt)
		VALUES ($1, $2, $3)
		ON CONFLICT (source_net, envelope_from, envelope_rcpt) DO UPDATE SET
			last_seen  = now(),
			-- stale rows restart the probation window
			first_seen = CASE WHEN greylist.last_seen < now() - make_interval(secs => $5)
			                  THEN now() ELSE greylist.first_seen END,
			pass_count = CASE WHEN greylist.last_seen < now() - make_interval(secs => $5)
			                  THEN 0 ELSE greylist.pass_count END
		RETURNING (now() - first_seen) >= make_interval(secs => $4), pass_count`
	var pass bool
	var passCount int64
	err := s.pool.QueryRow(ctx, q, sourceNet, from, rcpt,
		minDelay.Seconds(), staleAfter.Seconds()).Scan(&pass, &passCount)
	if err != nil {
		return false, fmt.Errorf("greylist check: %w", err)
	}
	// already-trusted triplets (passed before) skip the delay window even
	// right after a stale reset race — pass_count only grows on success
	if pass {
		if _, err := s.pool.Exec(ctx, `
			UPDATE greylist SET pass_count = pass_count + 1
			WHERE source_net = $1 AND envelope_from = $2 AND envelope_rcpt = $3`,
			sourceNet, from, rcpt); err != nil {
			return true, nil // count bump is best-effort
		}
	}

	// opportunistic pruning (~1% of calls) — keeps the table from growing
	// unbounded without a separate janitor goroutine
	if rand.IntN(100) == 0 {
		_, _ = s.pool.Exec(ctx,
			`DELETE FROM greylist WHERE last_seen < now() - make_interval(secs => $1)`,
			(30 * 24 * time.Hour).Seconds())
	}
	return pass, nil
}
