// Package store defines the domain types and interfaces of the mail storage engine.
//
// No SQL and no IMAP here — pure domain. The Postgres implementation
// (store/postgres) and the IMAP backend (internal/imap) depend on these
// interfaces. This makes it easy to later move body storage from DB to S3
// or plug in an in-memory implementation for tests.
package store

import (
	"context"
	"errors"
	"time"
)

// ── Errors ──────────────────────────────────────────────────

// ErrNotFound is returned when the lookup target does not exist. Implementations
// must return this sentinel so consumers (IMAP backend, etc.) can branch on it
// without importing the implementation package.
var ErrNotFound = errors.New("not found")

// ErrAuthFailed is an authentication failure.
var ErrAuthFailed = errors.New("authentication failed")

// ── Domain types ────────────────────────────────────────────

// Domain is a mail domain (top level of multi-tenancy). E.g. krisam.in
type Domain struct {
	ID        int64
	Name      string
	Active    bool
	CreatedAt time.Time

	// DKIM signing (Phase 2-4). No signing when Selector is empty.
	// Public key is published as a <selector>._domainkey.<name> TXT record.
	DKIMSelector   string
	DKIMPrivateKey string // PKCS#8 PEM

	// Outbound relay assignment (0005). nil = use default relay.
	RelayID *int64
}

// Relay is an SMTP relay for external delivery (Resend, SES, ...).
// Resolved in order: per-domain assignment (domain.relay_id) → default → env fallback.
type Relay struct {
	ID        int64
	Name      string // display name such as 'resend'
	Host      string
	Port      int
	Username  string
	Password  string // plaintext (never exposed via API — write-only)
	StartTLS  bool
	IsDefault bool
	Active    bool
	CreatedAt time.Time
}

// AccountKind values — account.kind column (0007).
const (
	AccountKindUser    = "user"    // human — OIDC login (JIT provisioning)
	AccountKindService = "service" // system — no login, address + app passwords only
)

// Account is a user = OIDC identity (0006). Addresses live separately in the address table.
// Humans log in via OIDC (JIT provisioning keyed by sub); mail apps use app passwords.
// Service accounts (0007) have a synthetic sub 'service:<email>' so web login is impossible.
type Account struct {
	ID          int64
	OIDCSubject string // OIDC sub claim (unique — the real identity key)
	OIDCEmail   string // email from the IdP (informational/display, refreshed on login)
	Kind        string // AccountKindUser | AccountKindService
	QuotaBytes  *int64 // nil = unlimited
	Active      bool
	CreatedAt   time.Time
}

// Mailbox is an IMAP folder (INBOX, Sent, ...).
type Mailbox struct {
	ID          int64
	AccountID   int64
	Name        string
	UIDValidity uint32 // fixed at creation. Changes on recreation → invalidates client caches
	UIDNext     uint32 // next UID to assign
	Subscribed  bool
	CreatedAt   time.Time
}

// Message is message metadata within a mailbox. Raw body is referenced via BlobID.
type Message struct {
	ID           int64
	MailboxID    int64
	UID          uint32 // mailbox-scoped. Monotonically increasing, never reused
	BlobID       int64
	SizeBytes    int64
	InternalDate time.Time // IMAP INTERNALDATE
	Subject      string    // header cache (for SEARCH/sorting)
	FromAddr     string
	Flags        []string // '\Seen', '\Flagged', ...
	CreatedAt    time.Time
}

// AppPassword is an app password for mail-app (IMAP/SMTP) authentication. Issued/revoked via OAuth.
type AppPassword struct {
	ID        int64
	AccountID int64
	Label     string // 'Thunderbird laptop'
	Hash      string // argon2id
	ScopeList []string
	LastUsed  *time.Time
	CreatedAt time.Time
	RevokedAt *time.Time
}

// Address is a mail address owned by an account. local_part '*' is the domain catch-all.
// All of a user's receiving/sending addresses live here (0006 — merged former account addresses + aliases).
type Address struct {
	ID        int64
	DomainID  int64
	LocalPart string // '*' = wildcard (all otherwise-unassigned addresses of that domain)
	AccountID int64
	CreatedAt time.Time

	// Convenience fields for lookups (filled via JOIN)
	DomainName   string // domain name of the address
	AccountEmail string // oidc_email of the owning account (for display)
}

// OutboundStatus is the state of an outbound queue item.
const (
	OutboundPending = "pending" // waiting to be sent (including retries)
	OutboundSent    = "sent"    // delivered
	OutboundFailed  = "failed"  // permanent failure (bounce candidate)
)

// OutboundMessage is one item in the outbound queue. Per-recipient (rcpt) —
// retries/failures are tracked independently per recipient.
type OutboundMessage struct {
	ID            int64
	EnvelopeFrom  string
	EnvelopeRcpt  string
	Raw           []byte
	Status        string
	AttemptCount  int
	NextAttemptAt time.Time
	LastError     string
	CreatedAt     time.Time
}

// MailboxStatus holds the aggregates required by SELECT/STATUS.
type MailboxStatus struct {
	MessageCount uint32
	UnseenCount  uint32
	NumRecent    uint32
	// DeletedCount is the number of \Deleted-flagged messages (STATUS DELETED).
	DeletedCount uint32
	// TotalBytes is the sum of message sizes (STATUS SIZE).
	TotalBytes  int64
	UIDNext     uint32
	UIDValidity uint32
}

// MailboxSummary is the webmail sidebar row (name + counts).
type MailboxSummary struct {
	Name         string
	MessageCount uint32
	UnseenCount  uint32
}

// FilterRule field values.
const (
	FilterFieldFrom    = "from"
	FilterFieldTo      = "to" // To + Cc
	FilterFieldSubject = "subject"
	FilterFieldHeader  = "header" // arbitrary header via HeaderName
)

// FilterRule match types (all case-insensitive).
const (
	FilterMatchContains = "contains"
	FilterMatchEquals   = "equals"
	FilterMatchPrefix   = "prefix"
	FilterMatchSuffix   = "suffix"
)

// FilterRule actions.
const (
	FilterActionMove     = "move"     // deliver to ActionMailbox instead of INBOX
	FilterActionMarkSeen = "markSeen" // deliver with \Seen
	FilterActionFlag     = "flag"     // deliver with \Flagged
	FilterActionDiscard  = "discard"  // drop silently (no DSN — the sender sees 250)
)

// FilterRule is one per-account delivery rule (0009). Rules run in position
// order on INBOX-bound delivery; the first matching active rule applies.
// Quarantine decisions (spam screening, DMARC) win over filters.
type FilterRule struct {
	ID            int64
	AccountID     int64
	Position      int
	Name          string
	Active        bool
	Field         string // FilterField*
	HeaderName    string // when Field == 'header'
	MatchType     string // FilterMatch*
	Pattern       string
	Action        string // FilterAction*
	ActionMailbox string // when Action == 'move'
	CreatedAt     time.Time
}

// ── Interfaces ──────────────────────────────────────────────

// Store is the top-level interface of the mail storage engine.
// The Postgres implementation satisfies it. IMAP/SMTP backends consume it.
type Store interface {
	// Authentication
	AuthenticateAppPassword(ctx context.Context, address, password string) (*Account, error)
	// FindAccountByAddress finds the active account that owns the address (exact
	// match only — no wildcards. Used for IMAP/SMTP login and self-service mapping).
	FindAccountByAddress(ctx context.Context, address string) (*Account, error)
	// FindAccountBySubject finds an active account by OIDC sub (web login identity).
	FindAccountBySubject(ctx context.Context, subject string) (*Account, error)
	// ResolveAddress finds the account to deliver to.
	// Priority: exact address > wildcard (*@domain).
	// Used by local delivery for SMTP receiving/submission.
	ResolveAddress(ctx context.Context, address string) (*Account, error)
	// CanSendAs reports whether the account may send as the given address
	// (owned addresses — including wildcard addresses).
	CanSendAs(ctx context.Context, accountID int64, address string) (bool, error)

	// Domains
	// FindDomain finds an active domain by name. Used during receiving/submission
	// to decide "is this our domain" (local delivery target).
	FindDomain(ctx context.Context, name string) (*Domain, error)

	// Mailboxes
	ListMailbox(ctx context.Context, accountID int64) ([]*Mailbox, error)
	GetMailbox(ctx context.Context, accountID int64, name string) (*Mailbox, error)
	CreateMailbox(ctx context.Context, accountID int64, name string) (*Mailbox, error)
	DeleteMailbox(ctx context.Context, accountID int64, name string) error
	RenameMailbox(ctx context.Context, accountID int64, name, newName string) error
	SetSubscribed(ctx context.Context, mailboxID int64, subscribed bool) error
	MailboxStatus(ctx context.Context, mailboxID int64) (*MailboxStatus, error)

	// Messages
	AppendMessage(ctx context.Context, mailboxID int64, raw []byte, flagList []string, internalDate time.Time) (*Message, error)
	ListMessage(ctx context.Context, mailboxID int64) ([]*Message, error)
	GetMessageBlob(ctx context.Context, messageID int64) ([]byte, error)
	SetFlag(ctx context.Context, messageID int64, flagList []string) error
	// ExpungeDeleted physically deletes \Deleted messages.
	// nil uids means all; otherwise only the given UIDs (for IMAP UID EXPUNGE).
	ExpungeDeleted(ctx context.Context, mailboxID int64, uids []uint32) ([]uint32, error)
	CopyMessage(ctx context.Context, messageID, destMailboxID int64) (*Message, error)

	// Outbound queue (Phase 2-3)
	// EnqueueOutbound enqueues an outbound item per recipient.
	EnqueueOutbound(ctx context.Context, from string, rcptList []string, raw []byte) error
	// DueOutbound fetches up to limit pending items whose send time has passed.
	// FOR UPDATE SKIP LOCKED semantics — multiple workers never grab the same row.
	DueOutbound(ctx context.Context, limit int) ([]*OutboundMessage, error)
	// MarkOutboundSent marks delivery success.
	MarkOutboundSent(ctx context.Context, id int64) error
	// MarkOutboundRetry records the failure + sets the next attempt time. attempts is incremented.
	MarkOutboundRetry(ctx context.Context, id int64, errMsg string, nextAttempt time.Time) error
	// MarkOutboundFailed marks a permanent failure (retries exhausted).
	MarkOutboundFailed(ctx context.Context, id int64, errMsg string) error

	// ResolveRelay finds the relay to use for the given sender domain name.
	// Domain-assigned relay → default relay → ErrNotFound (caller falls back to env).
	// Inactive relays are ignored.
	ResolveRelay(ctx context.Context, senderDomain string) (*Relay, error)

	// ListActiveFilterRule returns the account's active filter rules in
	// position order — the delivery path (SMTP inbound/submission) evaluates
	// these on INBOX-bound mail.
	ListActiveFilterRule(ctx context.Context, accountID int64) ([]*FilterRule, error)

	// CheckGreylist records/updates the (sourceNet, from, rcpt) triplet and
	// reports whether the message may pass (0010). First contact returns
	// false (caller answers 451); a retry at least minDelay after first_seen
	// passes and the triplet stays trusted. Rows idle longer than staleAfter
	// are treated as new. Errors must fail-open at the caller (an internal
	// error must never bounce mail).
	CheckGreylist(ctx context.Context, sourceNet, from, rcpt string, minDelay, staleAfter time.Duration) (bool, error)
}

// AdminStore is the extended interface used by the management plane (Admin API) (Phase 3).
// Kept separate from the protocol path (Store) so each surface stays narrow.
type AdminStore interface {
	Store

	// Domains
	ListDomain(ctx context.Context) ([]*Domain, error)
	CreateDomain(ctx context.Context, name string) (*Domain, error)
	// BackfillDomainAddress retroactively creates the primary address + INBOX for
	// existing human accounts whose oidc_email is on this domain (idempotent).
	// Returns the number created.
	BackfillDomainAddress(ctx context.Context, domainID int64) (int, error)
	SetDomainActive(ctx context.Context, id int64, active bool) error
	// SetDomainDKIM sets the DKIM selector/private key (empty strings = unset).
	SetDomainDKIM(ctx context.Context, id int64, selector, privateKeyPEM string) error

	// Accounts (user = OIDC identity. Human accounts are created via JIT provisioning only)
	ListAccount(ctx context.Context) ([]*Account, error)
	// ProvisionAccount does JIT provisioning keyed by OIDC sub — creates the
	// account if missing, otherwise just refreshes oidc_email and returns it (idempotent).
	// If the email domain is registered, it also creates the primary address + INBOX;
	// if not, only the account is created (no address = mail unusable, backfilled
	// when the domain is added).
	ProvisionAccount(ctx context.Context, subject, email string) (*Account, error)
	// CreateServiceAccount creates a service account (admin only) —
	// no login, address + app passwords only. The email address is registered as primary.
	CreateServiceAccount(ctx context.Context, email string) (*Account, error)
	SetAccountActive(ctx context.Context, id int64, active bool) error

	// App passwords (DD-02: issued after OAuth login)
	ListAppPassword(ctx context.Context, accountID int64) ([]*AppPassword, error)
	// ListAllAppPassword lists every app password (admin overview — avoids per-account fan-out).
	ListAllAppPassword(ctx context.Context) ([]*AppPassword, error)
	// CreateAppPassword stores the hash and returns the record.
	// Generating the plaintext is the caller's (API layer's) responsibility —
	// shown exactly once at issuance.
	CreateAppPassword(ctx context.Context, accountID int64, label, hash string) (*AppPassword, error)
	RevokeAppPassword(ctx context.Context, id int64) error

	// Addresses (account-owned mail addresses + wildcards — admin-only add/delete)
	ListAddress(ctx context.Context, domainID int64) ([]*Address, error)
	ListAccountAddress(ctx context.Context, accountID int64) ([]*Address, error)
	// ListAllAddress lists every address (admin overview — avoids per-account fan-out).
	ListAllAddress(ctx context.Context) ([]*Address, error)
	// CreateAddress treats localPart '*' as a catch-all.
	CreateAddress(ctx context.Context, domainID int64, localPart string, accountID int64) (*Address, error)
	// DeleteAddress deletes an address. The account's last regular address cannot
	// be deleted (prevents losing the receive/login mapping).
	DeleteAddress(ctx context.Context, id int64) error

	// Outbound queue management
	ListOutbound(ctx context.Context, status string, limit int) ([]*OutboundMessage, error)
	// RetryOutbound flips a failed item back to pending (due immediately).
	RetryOutbound(ctx context.Context, id int64) error
	// CancelOutbound cancels a pending item (races with an in-flight send lose —
	// a message already handed to the relay cannot be recalled).
	CancelOutbound(ctx context.Context, id int64) error
	// OutboundStat returns counts per status.
	OutboundStat(ctx context.Context) (map[string]int64, error)

	// relay (0005) — password is write-only (values returned by List are masked at the API layer too)
	ListRelay(ctx context.Context) ([]*Relay, error)
	CreateRelay(ctx context.Context, r *Relay) (*Relay, error)
	// UpdateRelay keeps the existing password when the password field is empty.
	UpdateRelay(ctx context.Context, r *Relay) (*Relay, error)
	DeleteRelay(ctx context.Context, id int64) error
	// SetDomainRelay assigns the domain's outbound relay (nil = use default).
	SetDomainRelay(ctx context.Context, domainID int64, relayID *int64) error

	// Global settings (0008) — key-value. First use: web display language (key='locale').
	// GetSetting returns store.ErrNotFound for a missing key.
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error

	// Webmail (account-scoped — ownership is enforced inside the queries,
	// so a guessed message ID of another account is a plain ErrNotFound).
	ListMailboxSummary(ctx context.Context, accountID int64) ([]*MailboxSummary, error)
	// ListMessagePage pages newest-first. beforeUID=0 starts at the top;
	// otherwise only uid < beforeUID rows return (cursor pagination).
	ListMessagePage(ctx context.Context, accountID int64, mailboxName string, limit int, beforeUID uint32) ([]*Message, error)
	// GetAccountMessage returns the message and the name of its mailbox.
	GetAccountMessage(ctx context.Context, accountID, messageID int64) (*Message, string, error)
	// MoveAccountMessage moves to another mailbox of the same account,
	// creating the destination on demand (Trash/Archive on first use).
	MoveAccountMessage(ctx context.Context, accountID, messageID int64, destName string) error
	// DeleteAccountMessage physically deletes (webmail uses it for Trash only).
	DeleteAccountMessage(ctx context.Context, accountID, messageID int64) error
	// SetAccountMessageFlag replaces flags with ownership check + notify.
	SetAccountMessageFlag(ctx context.Context, accountID, messageID int64, flagList []string) error
	// EnsureMailbox finds or creates a mailbox by name.
	EnsureMailbox(ctx context.Context, accountID int64, name string) (*Mailbox, error)

	// Filter rules (0009) CRUD — backs /api/me/filter. The delivery-path
	// read (ListActiveFilterRule) lives on Store.
	ListFilterRule(ctx context.Context, accountID int64) ([]*FilterRule, error)
	CreateFilterRule(ctx context.Context, r *FilterRule) (*FilterRule, error)
	// UpdateFilterRule rewrites the rule row (account-scoped by id+account).
	UpdateFilterRule(ctx context.Context, r *FilterRule) error
	DeleteFilterRule(ctx context.Context, accountID, id int64) error
	// SwapFilterRule swaps the positions of a rule and its neighbor
	// (direction -1 = up, +1 = down). No-op at the edges.
	SwapFilterRule(ctx context.Context, accountID, id int64, direction int) error
}
