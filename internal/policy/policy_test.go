package policy

import (
	"testing"

	"github.com/krisamin/mail/internal/store"
)

func account(mod func(*store.Account)) *store.Account {
	a := &store.Account{
		Active: true, CanSend: true, CanSendExternal: true, CanReceiveExternal: true,
	}
	if mod != nil {
		mod(a)
	}
	return a
}

func domain(send, receive bool) *store.Domain {
	return &store.Domain{AllowSendExternal: send, AllowReceiveExternal: receive}
}

func TestSend(t *testing.T) {
	caseList := []struct {
		name     string
		acct     *store.Account
		dom      *store.Domain
		external bool
		want     Reason
	}{
		{"default account sends anywhere", account(nil), domain(true, true), true, ReasonNone},
		{"internal-only still reaches colleagues",
			account(func(a *store.Account) { a.CanSendExternal = false }), domain(true, true), false, ReasonNone},
		{"internal-only blocked outside",
			account(func(a *store.Account) { a.CanSendExternal = false }), domain(true, true), true, ReasonExternalSend},
		{"domain switch blocks an allowed account",
			account(nil), domain(false, true), true, ReasonDomainSend},
		{"domain switch does not touch internal mail",
			account(nil), domain(false, true), false, ReasonNone},
		{"sending off blocks everything",
			account(func(a *store.Account) { a.CanSend = false }), domain(true, true), false, ReasonSendDisabled},
		{"disabled account",
			account(func(a *store.Account) { a.Active = false }), domain(true, true), false, ReasonInactive},
		{"unknown account", nil, domain(true, true), false, ReasonInactive},
		{"missing domain falls back to the account switch", account(nil), nil, true, ReasonNone},
	}
	for _, c := range caseList {
		t.Run(c.name, func(t *testing.T) {
			v := Send(c.acct, c.dom, c.external)
			if v.Reason != c.want {
				t.Fatalf("reason = %q, want %q", v.Reason, c.want)
			}
			if v.Allowed != (c.want == ReasonNone) {
				t.Fatalf("allowed = %v for reason %q", v.Allowed, v.Reason)
			}
			if !v.Allowed && v.Message == "" {
				t.Fatal("a refusal must carry a message for the SMTP peer")
			}
		})
	}
}

func TestReceive(t *testing.T) {
	caseList := []struct {
		name     string
		acct     *store.Account
		dom      *store.Domain
		external bool
		want     Reason
	}{
		{"default accepts outside mail", account(nil), domain(true, true), true, ReasonNone},
		{"internal-only refuses outside mail",
			account(func(a *store.Account) { a.CanReceiveExternal = false }), domain(true, true), true, ReasonExternalReceive},
		{"internal-only still gets internal mail",
			account(func(a *store.Account) { a.CanReceiveExternal = false }), domain(true, true), false, ReasonNone},
		{"domain refuses outside mail", account(nil), domain(true, false), true, ReasonDomainReceive},
		{"disabled account", account(func(a *store.Account) { a.Active = false }), domain(true, true), true, ReasonInactive},
	}
	for _, c := range caseList {
		t.Run(c.name, func(t *testing.T) {
			if got := Receive(c.acct, c.dom, c.external).Reason; got != c.want {
				t.Fatalf("reason = %q, want %q", got, c.want)
			}
		})
	}
}

func TestScope(t *testing.T) {
	if !Scope(nil, store.ScopeIMAP).Allowed {
		t.Fatal("an empty scope list must keep full access (passwords issued before scopes)")
	}
	if !Scope([]string{store.ScopeIMAP}, store.ScopeIMAP).Allowed {
		t.Fatal("matching scope must pass")
	}
	v := Scope([]string{store.ScopeIMAP}, store.ScopeSMTP)
	if v.Allowed || v.Reason != ReasonScopeMissing {
		t.Fatalf("read-only password must not send: %+v", v)
	}
}

func TestDailyLimit(t *testing.T) {
	limit := 10
	if !DailyLimit(nil, 999).Allowed {
		t.Fatal("no limit means no limit")
	}
	if !DailyLimit(&limit, 10).Allowed {
		t.Fatal("the limit itself is still allowed")
	}
	if DailyLimit(&limit, 11).Allowed {
		t.Fatal("one past the limit must be refused")
	}
	zero := 0
	if !DailyLimit(&zero, 500).Allowed {
		t.Fatal("zero is stored as unlimited")
	}
}
