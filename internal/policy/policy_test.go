package policy

import (
	"testing"

	"github.com/krisamin/mail/internal/store"
)

// defaultGroup is the floor: mail inside the server and mail from outside are
// fine, going out to the world is not.
func defaultGroup() *store.AccountGroup {
	return &store.AccountGroup{
		Name: "everyone", Position: 0, IsDefault: true,
		CanSend:            store.GrantAllow,
		CanSendExternal:    store.GrantDeny,
		CanReceiveExternal: store.GrantAllow,
	}
}

func group(name string, position int, mod func(*store.AccountGroup)) *store.AccountGroup {
	g := &store.AccountGroup{
		Name: name, Position: position,
		CanSend:            store.GrantInherit,
		CanSendExternal:    store.GrantInherit,
		CanReceiveExternal: store.GrantInherit,
	}
	if mod != nil {
		mod(g)
	}
	return g
}

func activeAccount() *store.Account { return &store.Account{Active: true} }

func domain(send, receive bool) *store.Domain {
	return &store.Domain{AllowSendExternal: send, AllowReceiveExternal: receive}
}

// The default group alone: colleagues yes, outside world no.
func TestResolveDefaultOnly(t *testing.T) {
	e := Resolve([]*store.AccountGroup{defaultGroup()})
	if !e.CanSend || e.CanSendExternal || !e.CanReceiveExternal {
		t.Fatalf("unexpected floor: %+v", e)
	}
	if e.DailySendLimit != nil {
		t.Fatal("no group set a limit")
	}
}

// A group above the floor that allows external sending wins.
func TestResolveGroupGrants(t *testing.T) {
	e := Resolve([]*store.AccountGroup{
		defaultGroup(),
		group("external-sender", 10, func(g *store.AccountGroup) { g.CanSendExternal = store.GrantAllow }),
	})
	if !e.CanSendExternal {
		t.Fatal("the group above the floor must grant external sending")
	}
	if len(e.GroupList) != 2 || e.GroupList[0] != "external-sender" {
		t.Fatalf("group list should read top-first: %v", e.GroupList)
	}
}

// Two groups disagree: the higher position decides, whatever order they
// arrive in.
func TestResolveHigherWins(t *testing.T) {
	allowLow := group("low", 10, func(g *store.AccountGroup) { g.CanSendExternal = store.GrantAllow })
	denyHigh := group("high", 20, func(g *store.AccountGroup) { g.CanSendExternal = store.GrantDeny })

	for _, order := range [][]*store.AccountGroup{
		{defaultGroup(), allowLow, denyHigh},
		{denyHigh, allowLow, defaultGroup()},
	} {
		if Resolve(order).CanSendExternal {
			t.Fatal("the higher group said deny and must win")
		}
	}

	// flip the positions and the answer flips with them
	allowLow.Position = 30
	if !Resolve([]*store.AccountGroup{defaultGroup(), allowLow, denyHigh}).CanSendExternal {
		t.Fatal("moving the allowing group to the top must win")
	}
}

// "inherit" is silence: it passes the question down instead of answering.
func TestResolveInheritFallsThrough(t *testing.T) {
	e := Resolve([]*store.AccountGroup{
		defaultGroup(),
		group("quiet", 50, nil),
	})
	if e.CanSendExternal {
		t.Fatal("a group with no opinion must not grant anything")
	}
	if !e.CanSend || !e.CanReceiveExternal {
		t.Fatal("the floor must still apply under a silent group")
	}
}

// The first group with a limit sets it; groups below are not consulted.
func TestResolveDailyLimit(t *testing.T) {
	ten, fifty := 10, 50
	e := Resolve([]*store.AccountGroup{
		defaultGroup(),
		group("bulk", 5, func(g *store.AccountGroup) { g.DailySendLimit = &fifty }),
		group("strict", 40, func(g *store.AccountGroup) { g.DailySendLimit = &ten }),
	})
	if e.DailySendLimit == nil || *e.DailySendLimit != 10 {
		t.Fatalf("the top group's limit must win: %+v", e.DailySendLimit)
	}
}

func TestSend(t *testing.T) {
	floor := Resolve([]*store.AccountGroup{defaultGroup()})
	sender := Resolve([]*store.AccountGroup{
		defaultGroup(),
		group("external-sender", 10, func(g *store.AccountGroup) { g.CanSendExternal = store.GrantAllow }),
	})

	caseList := []struct {
		name     string
		acct     *store.Account
		perm     Effective
		dom      *store.Domain
		external bool
		want     Reason
	}{
		{"floor reaches colleagues", activeAccount(), floor, domain(true, true), false, ReasonNone},
		{"floor stops at the border", activeAccount(), floor, domain(true, true), true, ReasonExternalSend},
		{"group opens the border", activeAccount(), sender, domain(true, true), true, ReasonNone},
		{"domain switch still wins", activeAccount(), sender, domain(false, true), true, ReasonDomainSend},
		{"domain switch ignores internal mail", activeAccount(), sender, domain(false, true), false, ReasonNone},
		{"disabled account", &store.Account{}, floor, domain(true, true), false, ReasonInactive},
		{"unknown account", nil, floor, domain(true, true), false, ReasonInactive},
	}
	for _, c := range caseList {
		t.Run(c.name, func(t *testing.T) {
			v := Send(c.acct, c.perm, c.dom, c.external)
			if v.Reason != c.want {
				t.Fatalf("reason = %q, want %q", v.Reason, c.want)
			}
			if !v.Allowed && v.Message == "" {
				t.Fatal("a refusal must carry a message for the SMTP peer")
			}
		})
	}
}

func TestReceive(t *testing.T) {
	floor := Resolve([]*store.AccountGroup{defaultGroup()})
	closed := Resolve([]*store.AccountGroup{
		defaultGroup(),
		group("no-inbound", 10, func(g *store.AccountGroup) { g.CanReceiveExternal = store.GrantDeny }),
	})

	if v := Receive(activeAccount(), floor, domain(true, true), true); !v.Allowed {
		t.Fatalf("the floor accepts outside mail: %+v", v)
	}
	if v := Receive(activeAccount(), closed, domain(true, true), true); v.Reason != ReasonExternalReceive {
		t.Fatalf("group denial must refuse outside mail: %+v", v)
	}
	if v := Receive(activeAccount(), closed, domain(true, true), false); !v.Allowed {
		t.Fatal("internal mail must still arrive")
	}
	if v := Receive(activeAccount(), floor, domain(true, false), true); v.Reason != ReasonDomainReceive {
		t.Fatalf("domain switch must refuse outside mail: %+v", v)
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
