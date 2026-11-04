package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// Mail events — a second LISTEN/NOTIFY channel next to mailbox_change.
//
// mailbox_change carries only a mailbox id (IMAP IDLE needs nothing more).
// mail_event carries what an outside consumer needs to decide whether to
// resync: the kind of change, the owning account, the mailbox name and
// UIDVALIDITY, and the UID when one message is concerned. The payload is
// built by Postgres from the mailbox row inside the same statement, so it is
// published at commit time together with the change itself (and not at all
// if the transaction rolls back).
//
// Consumers must treat events as hints: a LISTEN connection that drops loses
// whatever was sent meanwhile. The authoritative state is always the UID
// cursor (sync API), never the event stream.

const eventChannel = "mail_event"

// Mail event kinds.
const (
	EventNew     = "mail.new"     // a message appeared (delivery, append, copy, move-in)
	EventFlags   = "mail.flags"   // a message's flags changed
	EventExpunge = "mail.expunge" // message(s) left the mailbox (expunge, delete, move-out)
)

// MailEvent is one decoded mail_event notification.
type MailEvent struct {
	Kind        string    `json:"kind"`
	AccountID   uuid.UUID `json:"accountId"`
	Mailbox     string    `json:"mailbox"`
	UIDValidity uint32    `json:"uidValidity"`
	// UID is set when exactly one message is concerned; nil for bulk changes.
	UID        *uint32   `json:"uid"`
	OccurredAt time.Time `json:"occurredAt"`
}

// execer is satisfied by both the pool and a transaction.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// notifyEvent publishes a mail_event for one mailbox. uid nil = no single UID.
// Inside a transaction the notification is delivered at commit.
func notifyEvent(ctx context.Context, q execer, kind string, mailboxID uuid.UUID, uid *int64) error {
	_, err := q.Exec(ctx, `
		SELECT pg_notify('mail_event', json_build_object(
			'kind', $1::text,
			'accountId', mb.account_id,
			'mailbox', mb.name,
			'uidValidity', mb.uid_validity,
			'uid', $3::bigint,
			'occurredAt', clock_timestamp())::text)
		FROM mailbox mb WHERE mb.id = $2`,
		kind, mailboxID, uid)
	return err
}
