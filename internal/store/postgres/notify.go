package postgres

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Notifier — mailbox change notification hub based on Postgres LISTEN/NOTIFY.
//
// AppendMessage/ExpungeDeleted fire pg_notify('mailbox_change', <id>) at commit,
// and the Notifier LISTENs on a dedicated connection and broadcasts to
// per-mailbox subscribers (IMAP IDLE sessions). Immediate push instead of
// 15-second polling — polling remains only as the fallback when there is no
// subscription or a notification was lost.
//
// Under the single-replica assumption (current deployment) an in-process hub
// is sufficient, but since LISTEN/NOTIFY goes through the DB it keeps working
// with multiple replicas too.

const notifyChannel = "mailbox_change"

// Notifier is the mailbox change subscription hub.
type Notifier struct {
	store *Store

	mu     sync.Mutex
	subMap map[uuid.UUID]map[chan struct{}]bool // mailboxID → subscriber set
	// eventSubSet receives every mail_event (the webhook emitter).
	eventSubSet map[chan MailEvent]bool
}

// NewNotifier creates the hub. Run must be started in a separate goroutine for it to work.
func NewNotifier(st *Store) *Notifier {
	return &Notifier{
		store:       st,
		subMap:      map[uuid.UUID]map[chan struct{}]bool{},
		eventSubSet: map[chan MailEvent]bool{},
	}
}

// Subscribe returns a mailbox change channel. The second return value is the
// cancel function — must be called when IDLE ends (prevents leaks).
func (n *Notifier) Subscribe(mailboxID uuid.UUID) (<-chan struct{}, func()) {
	// buffer 1 — coalesce on notification bursts (IDLE only needs a "changed" signal)
	ch := make(chan struct{}, 1)
	n.mu.Lock()
	if n.subMap[mailboxID] == nil {
		n.subMap[mailboxID] = map[chan struct{}]bool{}
	}
	n.subMap[mailboxID][ch] = true
	n.mu.Unlock()

	cancel := func() {
		n.mu.Lock()
		delete(n.subMap[mailboxID], ch)
		if len(n.subMap[mailboxID]) == 0 {
			delete(n.subMap, mailboxID)
		}
		n.mu.Unlock()
	}
	return ch, cancel
}

// SubscribeEvent returns a channel receiving every mail_event (see event.go).
// buffer bounds how far the consumer may fall behind; past it events are
// dropped, never blocking the LISTEN loop. The second return value cancels.
func (n *Notifier) SubscribeEvent(buffer int) (<-chan MailEvent, func()) {
	if buffer < 1 {
		buffer = 1
	}
	ch := make(chan MailEvent, buffer)
	n.mu.Lock()
	n.eventSubSet[ch] = true
	n.mu.Unlock()
	return ch, func() {
		n.mu.Lock()
		delete(n.eventSubSet, ch)
		n.mu.Unlock()
	}
}

// dispatchEvent hands an event to every event subscriber without blocking.
func (n *Notifier) dispatchEvent(ev MailEvent) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for ch := range n.eventSubSet {
		select {
		case ch <- ev:
		default:
			log.Printf("notify: event subscriber full, dropped %s mailbox=%s", ev.Kind, ev.Mailbox)
		}
	}
}

// dispatch sends a non-blocking signal to all subscribers of the mailbox.
func (n *Notifier) dispatch(mailboxID uuid.UUID) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for ch := range n.subMap[mailboxID] {
		select {
		case ch <- struct{}{}:
		default: // a signal is already pending — coalesce
		}
	}
}

// Run runs the LISTEN loop until ctx is cancelled. If the connection drops it
// reconnects after a backoff (notifications lost meanwhile are absorbed by the
// IDLE fallback polling).
func (n *Notifier) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		if err := n.listenOnce(ctx); err != nil && ctx.Err() == nil {
			log.Printf("notify: LISTEN dropped (reconnect in %s): %v", backoff, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

func (n *Notifier) listenOnce(ctx context.Context) error {
	// LISTEN is per-connection — keep one from the pool held.
	conn, err := n.store.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN "+notifyChannel); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "LISTEN "+eventChannel); err != nil {
		return err
	}
	log.Printf("notify: LISTEN %s, %s started", notifyChannel, eventChannel)

	for {
		notification, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		if notification.Channel == eventChannel {
			var ev MailEvent
			if err := json.Unmarshal([]byte(notification.Payload), &ev); err != nil {
				log.Printf("notify: bad mail_event payload: %v", err)
				continue
			}
			n.dispatchEvent(ev)
			continue
		}
		id, err := uuid.Parse(notification.Payload)
		if err != nil {
			continue
		}
		n.dispatch(id)
	}
}
