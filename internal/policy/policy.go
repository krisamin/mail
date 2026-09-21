// Package policy decides what an account is allowed to do with mail.
//
// Two locks on the same door: crossing the server boundary needs both the
// account's own switch and its domain's switch. The domain switch is the
// operator's kill switch for a whole tenant; the account switch is the
// per-person setting. Anything staying inside this server (address to address
// we host) is never blocked by the external switches — turning a person
// "internal only" must not cut them off from their own colleagues.
//
// This package is pure: it reads the two records it is handed and returns a
// verdict. Counting (daily limits) and storage live in the store; wiring the
// verdict to an SMTP code lives at the call site.
package policy

import "github.com/krisamin/mail/internal/store"

// Reason identifies which rule refused — stable strings, used for metrics and
// log lines as well as the message shown to the other side.
type Reason string

const (
	ReasonNone             Reason = ""
	ReasonInactive         Reason = "account_inactive"
	ReasonSendDisabled     Reason = "send_disabled"
	ReasonExternalSend     Reason = "external_send_blocked"
	ReasonDomainSend       Reason = "domain_send_blocked"
	ReasonExternalReceive  Reason = "external_receive_blocked"
	ReasonDomainReceive    Reason = "domain_receive_blocked"
	ReasonDailyLimit       Reason = "daily_send_limit"
	ReasonScopeMissing     Reason = "scope_missing"
)

// Verdict is the answer. Message is safe to show an SMTP peer (it must not
// leak whether an address exists — these refusals are about permission, and
// the recipient is already known to exist by the time they are evaluated).
type Verdict struct {
	Allowed bool
	Reason  Reason
	Message string
}

var allow = Verdict{Allowed: true}

func deny(reason Reason, message string) Verdict {
	return Verdict{Reason: reason, Message: message}
}

// Send decides whether the account may send this message to one recipient.
// senderDomain is the domain of the address it is sending as; external is true
// when the recipient is not hosted here.
func Send(account *store.Account, senderDomain *store.Domain, external bool) Verdict {
	if account == nil {
		return deny(ReasonInactive, "unknown sender")
	}
	if !account.Active {
		return deny(ReasonInactive, "account is disabled")
	}
	if !account.CanSend {
		return deny(ReasonSendDisabled, "sending is disabled for this account")
	}
	if !external {
		return allow
	}
	if !account.CanSendExternal {
		return deny(ReasonExternalSend, "this account may only send inside this server")
	}
	if senderDomain != nil && !senderDomain.AllowSendExternal {
		return deny(ReasonDomainSend, "this domain may only send inside this server")
	}
	return allow
}

// Receive decides whether the account may accept this message. external is
// true when the message arrived from outside this server (the MX path);
// messages handed over inside the server skip the check.
func Receive(account *store.Account, recipientDomain *store.Domain, external bool) Verdict {
	if account == nil {
		return deny(ReasonInactive, "unknown recipient")
	}
	if !account.Active {
		return deny(ReasonInactive, "recipient is not accepting mail")
	}
	if !external {
		return allow
	}
	if !account.CanReceiveExternal {
		return deny(ReasonExternalReceive, "recipient does not accept mail from outside")
	}
	if recipientDomain != nil && !recipientDomain.AllowReceiveExternal {
		return deny(ReasonDomainReceive, "this domain does not accept mail from outside")
	}
	return allow
}

// Scope decides whether an app password carrying scopeList may be used for the
// protocol asking. An empty list means "issued before scopes existed" and is
// treated as full access — silently locking out every existing mail client on
// upgrade would be worse than the narrow permission it buys.
func Scope(scopeList []string, want string) Verdict {
	if len(scopeList) == 0 {
		return allow
	}
	for _, s := range scopeList {
		if s == want {
			return allow
		}
	}
	return deny(ReasonScopeMissing, "this app password is not allowed to "+want)
}

// DailyLimit turns a counter result into a verdict. used is the count after
// this send would be recorded.
func DailyLimit(limit *int, used int) Verdict {
	if limit == nil || *limit <= 0 || used <= *limit {
		return allow
	}
	return deny(ReasonDailyLimit, "daily sending limit reached")
}
