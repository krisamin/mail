// Package filter evaluates per-account delivery rules (0009).
//
// The SMTP delivery path calls Evaluate on INBOX-bound mail right before
// AppendMessage. Precedence: quarantine (spam screening / DMARC) beats
// filters — a message already headed to Junk is never re-routed by a user
// rule, so a "move to INBOX" rule cannot rescue quarantined spam.
package filter

import (
	"bytes"
	"context"
	"log"
	"mime"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/krisamin/mail/internal/store"
)

// evalTimeout bounds the rule lookup — filters must never stall delivery.
const evalTimeout = 5 * time.Second

// Verdict is the outcome of rule evaluation.
type Verdict struct {
	// Discard drops the message silently (delivery returns success).
	Discard bool
	// Mailbox is the delivery folder ("" = keep the caller's default).
	Mailbox string
	// FlagList are extra flags to deliver with (\Seen, \Flagged).
	FlagList []string
	// RuleName is the matched rule (log/debug).
	RuleName string
}

// Evaluate runs the account's active rules against the raw message and
// returns the first match's verdict. No match (or any error) returns the
// zero Verdict — fail-open, delivery proceeds as INBOX.
func Evaluate(ctx context.Context, st store.Store, accountID uuid.UUID, raw []byte) Verdict {
	ctx, cancel := context.WithTimeout(ctx, evalTimeout)
	defer cancel()

	ruleList, err := st.ListActiveFilterRule(ctx, accountID)
	if err != nil {
		log.Printf("filter: rule lookup failed account=%s: %v", accountID, err)
		return Verdict{}
	}
	if len(ruleList) == 0 {
		return Verdict{}
	}

	h := parseHeader(raw)
	for _, r := range ruleList {
		if matches(r, h) {
			return apply(r)
		}
	}
	return Verdict{}
}

// header is the pre-parsed view rules match against.
type header struct {
	from    string // From addresses, comma-joined, lowercased
	to      string // To + Cc addresses, comma-joined, lowercased
	subject string
	msg     *mail.Message // nil when unparseable
}

func parseHeader(raw []byte) header {
	var h header
	headerEnd := bytes.Index(raw, []byte("\r\n\r\n"))
	if headerEnd < 0 {
		headerEnd = len(raw)
	} else {
		headerEnd += 4
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw[:headerEnd]))
	if err != nil {
		return h
	}
	h.msg = msg
	h.subject = decodeHeader(msg.Header.Get("Subject"))
	h.from = strings.ToLower(addressListText(msg, "From"))
	toText := addressListText(msg, "To")
	if cc := addressListText(msg, "Cc"); cc != "" {
		if toText != "" {
			toText += ", "
		}
		toText += cc
	}
	h.to = strings.ToLower(toText)
	return h
}

// addressListText returns the bare addresses of a header, comma-joined.
// Falls back to the raw header text when parsing fails.
func addressListText(msg *mail.Message, key string) string {
	raw := msg.Header.Get(key)
	if raw == "" {
		return ""
	}
	list, err := msg.Header.AddressList(key)
	if err != nil {
		return raw
	}
	var b strings.Builder
	for i, a := range list {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(a.Address)
	}
	return b.String()
}

// decodeHeader decodes RFC 2047 encoded-words (=?utf-8?b?...?=) so patterns
// match what the user sees, not the wire encoding.
func decodeHeader(s string) string {
	dec := new(mime.WordDecoder)
	out, err := dec.DecodeHeader(s)
	if err != nil {
		return s
	}
	return out
}

// matches evaluates one rule against the parsed header (case-insensitive).
func matches(r *store.FilterRule, h header) bool {
	var value string
	switch r.Field {
	case store.FilterFieldFrom:
		value = h.from
	case store.FilterFieldTo:
		value = h.to
	case store.FilterFieldSubject:
		value = strings.ToLower(h.subject)
	case store.FilterFieldHeader:
		if h.msg == nil {
			return false
		}
		value = strings.ToLower(decodeHeader(h.msg.Header.Get(r.HeaderName)))
	default:
		return false
	}
	pattern := strings.ToLower(r.Pattern)
	switch r.MatchType {
	case store.FilterMatchContains:
		return strings.Contains(value, pattern)
	case store.FilterMatchEquals:
		return value == pattern
	case store.FilterMatchPrefix:
		return strings.HasPrefix(value, pattern)
	case store.FilterMatchSuffix:
		return strings.HasSuffix(value, pattern)
	default:
		return false
	}
}

func apply(r *store.FilterRule) Verdict {
	v := Verdict{RuleName: r.Name}
	switch r.Action {
	case store.FilterActionDiscard:
		v.Discard = true
	case store.FilterActionMove:
		v.Mailbox = r.ActionMailbox
	case store.FilterActionMarkSeen:
		v.FlagList = []string{"\\Seen"}
	case store.FilterActionFlag:
		v.FlagList = []string{"\\Flagged"}
	}
	return v
}
