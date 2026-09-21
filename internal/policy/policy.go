// Package policy decides what an account is allowed to do with mail.
//
// Permissions live on groups, not on people. One default group is the floor
// everybody stands on; extra groups stack on top, and for each rule the
// highest group that has an opinion wins. A group that says nothing about a
// rule ("inherit") passes the question down.
//
// On top of that sits the domain, which owns a kill switch for crossing the
// server boundary: the operator can shut a whole tenant off regardless of
// what its groups say. Anything staying inside this server — address to
// address we host — is never blocked by the external switches, because
// cutting someone off from their own colleagues is isolation, not permission.
//
// This package is pure: it reads the records it is handed and returns a
// verdict. Counting (daily limits) and storage live in the store; turning a
// verdict into an SMTP code happens at the call site.
package policy

import (
	"sort"

	"github.com/krisamin/mail/internal/store"
)

// Reason identifies which rule refused — stable strings, used for metrics and
// log lines as well as the message shown to the other side.
type Reason string

const (
	ReasonNone            Reason = ""
	ReasonInactive        Reason = "account_inactive"
	ReasonSendDisabled    Reason = "send_disabled"
	ReasonExternalSend    Reason = "external_send_blocked"
	ReasonDomainSend      Reason = "domain_send_blocked"
	ReasonExternalReceive Reason = "external_receive_blocked"
	ReasonDomainReceive   Reason = "domain_receive_blocked"
	ReasonDailyLimit      Reason = "daily_send_limit"
	ReasonScopeMissing    Reason = "scope_missing"
)

// Verdict is the answer. Message is safe to show an SMTP peer: these refusals
// are about permission, and the recipient's existence is already settled by
// the time they are evaluated, so nothing leaks.
type Verdict struct {
	Allowed bool
	Reason  Reason
	Message string
}

var allow = Verdict{Allowed: true}

func deny(reason Reason, message string) Verdict {
	return Verdict{Reason: reason, Message: message}
}

// Effective is what a stack of groups adds up to for one account.
type Effective struct {
	CanSend            bool
	CanSendExternal    bool
	CanReceiveExternal bool
	DailySendLimit     *int
	// GroupList names the groups that produced this, top first (for the UI).
	GroupList []string
}

// Resolve folds a group stack into one answer. Groups may arrive in any
// order; the highest position wins, ties break on name so the result is
// stable. A rule nobody has an opinion about lands on false — a group that
// grants nothing grants nothing.
func Resolve(groupList []*store.AccountGroup) Effective {
	ordered := make([]*store.AccountGroup, len(groupList))
	copy(ordered, groupList)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Position != ordered[j].Position {
			return ordered[i].Position > ordered[j].Position
		}
		return ordered[i].Name < ordered[j].Name
	})

	out := Effective{}
	for _, g := range ordered {
		out.GroupList = append(out.GroupList, g.Name)
	}

	decide := func(pick func(*store.AccountGroup) string) bool {
		for _, g := range ordered {
			switch pick(g) {
			case store.GrantAllow:
				return true
			case store.GrantDeny:
				return false
			}
		}
		return false
	}
	out.CanSend = decide(func(g *store.AccountGroup) string { return g.CanSend })
	out.CanSendExternal = decide(func(g *store.AccountGroup) string { return g.CanSendExternal })
	out.CanReceiveExternal = decide(func(g *store.AccountGroup) string { return g.CanReceiveExternal })

	for _, g := range ordered {
		if g.DailySendLimit != nil {
			limit := *g.DailySendLimit
			out.DailySendLimit = &limit
			break
		}
	}
	return out
}

// Send decides whether the account may send this message to one recipient.
// senderDomain is the domain of the address it is sending as; external is
// true when the recipient is not hosted here.
func Send(account *store.Account, perm Effective, senderDomain *store.Domain, external bool) Verdict {
	if account == nil {
		return deny(ReasonInactive, "unknown sender")
	}
	if !account.Active {
		return deny(ReasonInactive, "account is disabled")
	}
	if !perm.CanSend {
		return deny(ReasonSendDisabled, "sending is disabled for this account")
	}
	if !external {
		return allow
	}
	if !perm.CanSendExternal {
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
func Receive(account *store.Account, perm Effective, recipientDomain *store.Domain, external bool) Verdict {
	if account == nil {
		return deny(ReasonInactive, "unknown recipient")
	}
	if !account.Active {
		return deny(ReasonInactive, "recipient is not accepting mail")
	}
	if !external {
		return allow
	}
	if !perm.CanReceiveExternal {
		return deny(ReasonExternalReceive, "recipient does not accept mail from outside")
	}
	if recipientDomain != nil && !recipientDomain.AllowReceiveExternal {
		return deny(ReasonDomainReceive, "this domain does not accept mail from outside")
	}
	return allow
}

// Scope decides whether an app password carrying scopeList may be used for
// the protocol asking. An empty list means "issued before scopes existed" and
// keeps full access — silently locking out every existing mail client on
// upgrade would cost more than the narrow permission buys.
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
