// Package delivery is the single local delivery pipeline.
//
// Every path that puts a message into a local account's mailbox — SMTP
// inbound, SMTP submission (local recipients), webmail send — goes through
// Deliver so filter evaluation, folder resolution/creation, and the append
// (which fires the IDLE notification) can never drift apart between entry
// points. Quarantine decisions (spam screening, DMARC) are made by the
// caller before Deliver and expressed via the Folder option: filters only
// run on INBOX-bound mail, so a user rule can't rescue quarantined spam.
package delivery

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/krisamin/mail/internal/filter"
	"github.com/krisamin/mail/internal/metric"
	"github.com/krisamin/mail/internal/store"
)

// Store is the slice of store.Store the pipeline needs (consumer-side
// interface — both store.Store and store.AdminStore satisfy it).
type Store interface {
	ListActiveFilterRule(ctx context.Context, accountID uuid.UUID) ([]*store.FilterRule, error)
	GetMailbox(ctx context.Context, accountID uuid.UUID, name string) (*store.Mailbox, error)
	CreateMailbox(ctx context.Context, accountID uuid.UUID, name string) (*store.Mailbox, error)
	AppendMessage(ctx context.Context, mailboxID uuid.UUID, raw []byte, flagList []string, internalDate time.Time) (*store.Message, error)
	QuotaExceeded(ctx context.Context, accountID uuid.UUID, addBytes int64) (bool, error)
}

// ErrQuotaExceeded is returned when the recipient's storage quota would be
// exceeded. SMTP callers translate it to 452 4.2.2 (mailbox full, transient
// per RFC 3463 — the sender may retry after the user frees space).
var ErrQuotaExceeded = errors.New("recipient quota exceeded")

// Request is one local delivery.
type Request struct {
	AccountID uuid.UUID
	Address   string // recipient address (logging only)
	Origin    string // entry point tag for logs: "smtp", "submission", "webmail"
	Folder    string // destination folder; "" or "INBOX" runs filter rules
	FlagList  []string
	Raw       []byte
	Date      time.Time // internal date; zero value = now
}

// Deliver runs the pipeline: quota → filters (INBOX only) → ensure folder →
// append. A filter discard returns (true, nil): the message was intentionally
// dropped and the SMTP transaction/API call must still succeed.
func Deliver(ctx context.Context, st Store, req Request) (discarded bool, err error) {
	// quota gate first — a full mailbox rejects before any rule runs.
	// Fail-open on check errors: quota protects disk space, it must not
	// bounce mail when the check itself is broken.
	if over, qerr := st.QuotaExceeded(ctx, req.AccountID, int64(len(req.Raw))); qerr != nil {
		log.Printf("%s: quota check failed account=%s (delivering anyway): %v", req.Origin, req.AccountID, qerr)
	} else if over {
		log.Printf("%s: quota exceeded to=%s size=%d", req.Origin, req.Address, len(req.Raw))
		metric.DeliveryTotal.WithLabelValues(req.Origin, "quota").Inc()
		return false, ErrQuotaExceeded
	}

	folder := req.Folder
	if folder == "" {
		folder = "INBOX"
	}
	flagList := req.FlagList

	if folder == "INBOX" {
		v := filter.Evaluate(ctx, st, req.AccountID, req.Raw)
		if v.Discard {
			log.Printf("%s: filter discard rule=%q to=%s", req.Origin, v.RuleName, req.Address)
			metric.DeliveryTotal.WithLabelValues(req.Origin, "discarded").Inc()
			return true, nil
		}
		if v.Mailbox != "" {
			folder = v.Mailbox
			log.Printf("%s: filter move rule=%q to=%s folder=%s", req.Origin, v.RuleName, req.Address, folder)
		}
		flagList = append(flagList, v.FlagList...)
	}

	box, err := st.GetMailbox(ctx, req.AccountID, folder)
	if errors.Is(err, store.ErrNotFound) {
		box, err = st.CreateMailbox(ctx, req.AccountID, folder)
	}
	if err != nil {
		metric.DeliveryTotal.WithLabelValues(req.Origin, "error").Inc()
		return false, fmt.Errorf("ensure %s: %w", folder, err)
	}

	date := req.Date
	if date.IsZero() {
		date = time.Now()
	}
	if _, err := st.AppendMessage(ctx, box.ID, req.Raw, flagList, date); err != nil {
		metric.DeliveryTotal.WithLabelValues(req.Origin, "error").Inc()
		return false, fmt.Errorf("append: %w", err)
	}
	metric.DeliveryTotal.WithLabelValues(req.Origin, "delivered").Inc()
	return false, nil
}
