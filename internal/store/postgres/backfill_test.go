package postgres

import (
	"testing"

	"github.com/krisamin/mail/internal/store"
)

// TestBackfillDomainAddress: verifies that a (bare) account that logged in
// without a domain retroactively receives address+INBOX when the domain is added.
func TestBackfillDomainAddress(t *testing.T) {
	st := addressTestStore(t)
	ctx := t.Context()

	// 1) two users log in while domain is unregistered → bare accounts
	alice, err := st.ProvisionAccount(ctx, "sub-alice", "alice@late.example")
	if err != nil {
		t.Fatalf("alice: %v", err)
	}
	bob, err := st.ProvisionAccount(ctx, "sub-bob", "bob@late.example")
	if err != nil {
		t.Fatalf("bob: %v", err)
	}
	// user on another domain — not a backfill target
	other, err := st.ProvisionAccount(ctx, "sub-other", "other@elsewhere.example")
	if err != nil {
		t.Fatalf("other: %v", err)
	}

	// 2) add domain + backfill
	dom, err := st.CreateDomain(ctx, "late.example")
	if err != nil {
		t.Fatalf("domain: %v", err)
	}
	created, err := st.BackfillDomainAddress(ctx, dom.ID)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if created != 2 {
		t.Fatalf("backfill should cover 2 users: %d", created)
	}

	// 3) alice/bob get address+INBOX, other stays unchanged
	for _, u := range []*store.Account{alice, bob} {
		addressList, err := st.ListAccountAddress(ctx, u.ID)
		if err != nil || len(addressList) != 1 {
			t.Fatalf("%s should have 1 address: %v %+v", u.OIDCEmail, err, addressList)
		}
		boxList, err := st.ListMailbox(ctx, u.ID)
		if err != nil || len(boxList) != 1 || boxList[0].Name != "INBOX" {
			t.Fatalf("%s should have INBOX: %v %+v", u.OIDCEmail, err, boxList)
		}
	}
	if addressList, _ := st.ListAccountAddress(ctx, other.ID); len(addressList) != 0 {
		t.Fatalf("other must have no addresses: %+v", addressList)
	}

	// 4) idempotency — rerun yields 0
	again, err := st.BackfillDomainAddress(ctx, dom.ID)
	if err != nil || again != 0 {
		t.Fatalf("rerun must yield 0: %v %d", err, again)
	}

	// 5) delivery path check — routing via the backfilled address
	if u, err := st.FindAccountByAddress(ctx, "alice@late.example"); err != nil || u.ID != alice.ID {
		t.Fatalf("backfilled address routing: %v", err)
	}
	t.Log("✔ retroactive provisioning on domain add (2 created, idempotent, routing)")
}
