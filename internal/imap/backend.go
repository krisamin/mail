// Package imap implements go-imap v2 imapserver.Session on top of store.Store.
//
// The protocol state machine (command parsing, literals, response encoding)
// is handled by go-imap; here we only fill in "what data to return"
// (DD-01 two-layer architecture).
//
// ★Phase 1 concurrency model — session snapshot:
// At SELECT time we snapshot the mailbox's UID list into session memory.
// Sequence number = snapshot index + 1. Changes made by other sessions
// (new mail, expunge) are reflected by comparing the snapshot against the DB
// in Poll/Idle. Real-time push between sessions is delivered via Postgres
// LISTEN/NOTIFY (see MailboxNotifier).
package imap

import (
	"context"
	"net"
	"time"

	"github.com/google/uuid"

	"github.com/emersion/go-imap/v2/imapserver"

	"github.com/krisamin/mail/internal/guard"
	"github.com/krisamin/mail/internal/store"
)

// opTimeout caps a single IMAP command's store access.
const opTimeout = 30 * time.Second

// Backend is the IMAP session factory wrapping the store.
type Backend struct {
	store   store.Store
	limiter *guard.Limiter // auth brute-force protection (per IP)
	// notifier is the mailbox-change push hub (nil = IDLE falls back to polling only).
	notifier MailboxNotifier
}

// MailboxNotifier is the mailbox-change subscription interface (implemented by postgres.Notifier).
type MailboxNotifier interface {
	Subscribe(mailboxID uuid.UUID) (<-chan struct{}, func())
}

// NewBackend creates an IMAP backend on top of the store.
func NewBackend(st store.Store) *Backend {
	return &Backend{store: st, limiter: guard.NewLimiter()}
}

// WithNotifier attaches the LISTEN/NOTIFY hub — IDLE wakes on push instead of polling.
func (b *Backend) WithNotifier(n MailboxNotifier) *Backend {
	b.notifier = n
	return b
}

// NewSession is the callback plugged into imapserver.Options.NewSession.
func (b *Backend) NewSession(c *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
	remoteIP := ""
	if c != nil && c.NetConn() != nil {
		if host, _, err := net.SplitHostPort(c.NetConn().RemoteAddr().String()); err == nil {
			remoteIP = host
		}
	}
	return &Session{backend: b, remoteIP: remoteIP}, nil, nil
}

// opCtx creates a per-command context.
func opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), opTimeout)
}
