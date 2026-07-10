// Package guard is an in-memory rate limiter for auth brute-force protection.
//
// Per-key sliding window: once failures exceed the threshold inside the
// window, the key is blocked for a period. Repeated blocks grow the block
// duration exponentially (BlockFor → 2x → 4x … capped by MaxBlock).
// A success clears the key's record.
//
// Keys are usually IPs, but an account key is tracked alongside to defend
// against distributed brute force (many IPs → one account); callers
// distinguish them with "ip:"/"acct:" prefixes. IPv6 is normalized to its
// /64 prefix to defeat interface-ID rotation.
//
// Single-instance, memory-backed — multiple replicas count independently,
// which is still sufficient for the goal (stopping unlimited attempts).
package guard

import (
	"net"
	"sync"
	"time"
)

// Limiter tracks auth failures per key.
type Limiter struct {
	mu    sync.Mutex
	entry map[string]*entry

	// MaxFailure is the allowed failure count within the window (block on exceed).
	MaxFailure int
	// Window is the failure-counting window.
	Window time.Duration
	// BlockFor is the initial block duration (grows exponentially on repeat blocks).
	BlockFor time.Duration
	// MaxBlock caps the exponential growth.
	MaxBlock time.Duration

	timeNow func() time.Time // test override
}

type entry struct {
	failureCount int
	windowStart  time.Time
	blockedUntil time.Time
	blockCount   int // cumulative block count — the exponential backoff exponent
}

// NewLimiter creates a limiter with default parameters:
// 10 failures in 15 minutes → 15-minute block; repeats: 30m→1h→2h→4h (cap).
func NewLimiter() *Limiter {
	return &Limiter{
		entry:      map[string]*entry{},
		MaxFailure: 10,
		Window:     15 * time.Minute,
		BlockFor:   15 * time.Minute,
		MaxBlock:   4 * time.Hour,
		timeNow:    time.Now,
	}
}

// KeyForIP normalizes an IP string into a limiter key.
// IPv6 collapses to its /64 prefix — preventing a single line from evading
// the count by rotating interface IDs. IPv4 (and 4-in-6) stays as-is.
func KeyForIP(ipStr string) string {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ipStr
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// Allow decides whether the key may attempt authentication now.
func (l *Limiter) Allow(key string) bool {
	if key == "" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.entry[key]
	if !ok {
		return true
	}
	now := l.timeNow()
	if now.Before(e.blockedUntil) {
		return false
	}
	// window expired + no block history → full reset
	// (with block history the entry stays to preserve blockCount)
	if now.Sub(e.windowStart) > l.Window && e.blockCount == 0 {
		delete(l.entry, key)
	}
	return true
}

// Fail records an auth failure. Reaching the threshold starts a block.
func (l *Limiter) Fail(key string) {
	if key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.timeNow()
	e, ok := l.entry[key]
	if !ok {
		e = &entry{windowStart: now}
		l.entry[key] = e
	} else if now.Sub(e.windowStart) > l.Window {
		// reset the window only — blockCount (the exponent) survives
		e.failureCount = 0
		e.windowStart = now
	}
	e.failureCount++
	if e.failureCount >= l.MaxFailure {
		e.blockedUntil = now.Add(l.blockDuration(e.blockCount))
		e.blockCount++
		// starting a block resets the window too (retry count restarts after unblock)
		e.failureCount = 0
		e.windowStart = now
	}

	// lazy cleanup — keeps the map from growing (drop expired entries)
	if len(l.entry) > 10000 {
		for k, v := range l.entry {
			if now.Sub(v.windowStart) > l.Window && now.After(v.blockedUntil) {
				delete(l.entry, k)
			}
		}
	}
}

// blockDuration is the duration of the nth (0-based) block — BlockFor * 2^n, capped by MaxBlock.
func (l *Limiter) blockDuration(n int) time.Duration {
	d := l.BlockFor
	for i := 0; i < n; i++ {
		d *= 2
		if l.MaxBlock > 0 && d >= l.MaxBlock {
			return l.MaxBlock
		}
	}
	if l.MaxBlock > 0 && d > l.MaxBlock {
		return l.MaxBlock
	}
	return d
}

// Success clears the key's failure record on successful auth.
func (l *Limiter) Success(key string) {
	if key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entry, key)
}
