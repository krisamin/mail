package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/krisamin/mail/internal/store"
)

// Address system integration test (migration 0006 — account=identity, address=address).
// Scenario: two domains krisam.in + kirby.so; attach multiple addresses
// + catch-all(*@kirby.so) to maro/guest accounts and verify resolution.

func addressTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("MAIL_TEST_DSN")
	if dsn == "" {
		t.Skip("MAIL_TEST_DSN not set — skipping integration test")
	}
	st, err := New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(st.Close)
	_, _ = st.Pool().Exec(context.Background(),
		`TRUNCATE domain, account, app_password, mailbox, message, message_flag, message_blob, outbound_queue, address, relay RESTART IDENTITY CASCADE`)
	return st
}

func TestAddressModel(t *testing.T) {
	st := addressTestStore(t)
	ctx := context.Background()

	krisam, err := st.CreateDomain(ctx, "krisam.in")
	if err != nil {
		t.Fatalf("krisam.in: %v", err)
	}
	_ = krisam
	kirby, err := st.CreateDomain(ctx, "kirby.so")
	if err != nil {
		t.Fatalf("kirby.so: %v", err)
	}

	// 1) JIT provisioning — account creation + primary address + INBOX
	maro, err := st.ProvisionAccount(ctx, "sub-maro", "maro@krisam.in")
	if err != nil {
		t.Fatalf("maro provisioning: %v", err)
	}
	guest, err := st.ProvisionAccount(ctx, "sub-guest", "guest@krisam.in")
	if err != nil {
		t.Fatalf("guest provisioning: %v", err)
	}
	boxList, err := st.ListMailbox(ctx, maro.ID)
	if err != nil || len(boxList) != 1 || boxList[0].Name != "INBOX" {
		t.Fatalf("provisioned INBOX: %v %+v", err, boxList)
	}
	t.Log("✔ JIT provisioning (account+address+INBOX)")

	// Idempotency: re-calling with the same sub → same account, email refreshed
	again, err := st.ProvisionAccount(ctx, "sub-maro", "maro@krisam.in")
	if err != nil || again.ID != maro.ID {
		t.Fatalf("idempotent provisioning: %v %+v", err, again)
	}
	// Unregistered domain → bare account (no address/INBOX, login still allowed)
	bare, err := st.ProvisionAccount(ctx, "sub-x", "x@example.com")
	if err != nil {
		t.Fatalf("unregistered domain should still create an account: %v", err)
	}
	if addressList, err := st.ListAccountAddress(ctx, bare.ID); err != nil || len(addressList) != 0 {
		t.Fatalf("bare account must have no addresses: %v %+v", err, addressList)
	}
	if boxList, err := st.ListMailbox(ctx, bare.ID); err != nil || len(boxList) != 0 {
		t.Fatalf("bare account must have no mailboxes: %v %+v", err, boxList)
	}
	// Adoption: new sub with the same email (IdP user recreated) → takes over existing account
	adopted, err := st.ProvisionAccount(ctx, "sub-maro-v2", "maro@krisam.in")
	if err != nil || adopted.ID != maro.ID || adopted.OIDCSubject != "sub-maro-v2" {
		t.Fatalf("adoption provisioning: %v %+v", err, adopted)
	}
	// Revert (subsequent cases assume sub-maro)
	if _, err := st.ProvisionAccount(ctx, "sub-maro", "maro@krisam.in"); err != nil {
		t.Fatalf("adoption revert: %v", err)
	}
	t.Log("✔ idempotency + unregistered domain rejection + same-email adoption")

	// 2) sub/address lookup
	if u, err := st.FindAccountBySubject(ctx, "sub-maro"); err != nil || u.ID != maro.ID {
		t.Fatalf("FindAccountBySubject: %v", err)
	}
	if u, err := st.FindAccountByAddress(ctx, "maro@krisam.in"); err != nil || u.ID != maro.ID {
		t.Fatalf("FindAccountByAddress: %v", err)
	}
	t.Log("✔ sub/address lookup")

	// 3) admin adds an address: test@kirby.so → maro (cross-domain)
	if _, err := st.CreateAddress(ctx, kirby.ID, "test", maro.ID); err != nil {
		t.Fatalf("address add: %v", err)
	}
	u, err := st.ResolveAddress(ctx, "test@kirby.so")
	if err != nil || u.ID != maro.ID {
		t.Fatalf("test@kirby.so should resolve to maro: %v", err)
	}
	t.Log("✔ admin address add + cross-domain resolution")

	// 4) catch-all: *@kirby.so → maro
	if _, err := st.CreateAddress(ctx, kirby.ID, "*", maro.ID); err != nil {
		t.Fatalf("catch-all: %v", err)
	}
	u, err = st.ResolveAddress(ctx, "anything@kirby.so")
	if err != nil || u.ID != maro.ID {
		t.Fatalf("anything@kirby.so should resolve to maro: %v", err)
	}
	t.Log("✔ wildcard catch-all resolution")

	// 5) priority: exact address > wildcard
	if _, err := st.CreateAddress(ctx, kirby.ID, "gyestt", guest.ID); err != nil {
		t.Fatalf("kirby exact address: %v", err)
	}
	u, err = st.ResolveAddress(ctx, "gyestt@kirby.so")
	if err != nil || u.ID != guest.ID {
		t.Fatalf("exact address must take priority over catch-all: %v", err)
	}
	// Recreating an occupied address → duplicate
	if _, err := st.CreateAddress(ctx, kirby.ID, "gyestt", maro.ID); err == nil {
		t.Fatal("recreating an occupied address must not succeed")
	}
	t.Log("✔ exact > wildcard priority + duplicate rejection")

	// 6) local with no address → ErrNotFound (krisam.in has no catch-all)
	if _, err := st.ResolveAddress(ctx, "nobody@krisam.in"); err != store.ErrNotFound {
		t.Fatalf("nobody@krisam.in should be NotFound: %v", err)
	}
	t.Log("✔ unregistered address NotFound (domain without catch-all)")

	// 7) CanSendAs
	for _, tc := range []struct {
		accountID int64
		addr      string
		want      bool
	}{
		{maro.ID, "maro@krisam.in", true},   // primary
		{maro.ID, "test@kirby.so", true},    // additional address
		{maro.ID, "random@kirby.so", true},  // own catch-all
		{guest.ID, "test@kirby.so", false},  // someone else's address
		{guest.ID, "gyestt@kirby.so", true}, // own address (kirby)
		{maro.ID, "gyestt@kirby.so", false}, // guest's exact address — exact wins even with catch-all
		{maro.ID, "x@nowhere.com", false},   // external
	} {
		got, err := st.CanSendAs(ctx, tc.accountID, tc.addr)
		if err != nil || got != tc.want {
			t.Fatalf("CanSendAs(%d, %s) = %v (want %v): %v", tc.accountID, tc.addr, got, tc.want, err)
		}
	}
	t.Log("✔ CanSendAs 7 cases")

	// 8) list/delete (maro: primary + test@kirby.so + catch-all = 3)
	addressList, err := st.ListAccountAddress(ctx, maro.ID)
	if err != nil || len(addressList) != 3 {
		t.Fatalf("maro should have 3 addresses: %v %d", err, len(addressList))
	}
	if addressList[0].DomainName == "" || addressList[0].AccountEmail == "" {
		t.Fatal("JOIN convenience fields are empty")
	}
	domainAddressList, err := st.ListAddress(ctx, kirby.ID)
	if err != nil || len(domainAddressList) != 3 {
		t.Fatalf("kirby.so should have 3 addresses: %v %d", err, len(domainAddressList))
	}

	// Deleting the last regular address is refused — if guest deletes gyestt only primary remains.
	// Deleting guest's primary (guest@krisam.in) is OK because gyestt exists,
	// then deleting gyestt (the last one) must be refused.
	var guestPrimaryID int64
	guestAddressList, _ := st.ListAccountAddress(ctx, guest.ID)
	for _, a := range guestAddressList {
		if a.DomainName == "krisam.in" {
			guestPrimaryID = a.ID
		}
	}
	if err := st.DeleteAddress(ctx, guestPrimaryID); err != nil {
		t.Fatalf("guest primary delete (allowed, another address exists): %v", err)
	}
	guestAddressList, _ = st.ListAccountAddress(ctx, guest.ID)
	if len(guestAddressList) != 1 {
		t.Fatalf("guest should have 1 address left: %d", len(guestAddressList))
	}
	if err := st.DeleteAddress(ctx, guestAddressList[0].ID); err == nil {
		t.Fatal("deleting the last regular address must not succeed")
	}
	t.Log("✔ listing (JOIN fields) + last-address deletion guard")

	// catch-all can be deleted even if it's the last one (not a regular address)
	var catchAllID int64
	maroAddressList, _ := st.ListAccountAddress(ctx, maro.ID)
	for _, a := range maroAddressList {
		if a.LocalPart == "*" {
			catchAllID = a.ID
		}
	}
	if err := st.DeleteAddress(ctx, catchAllID); err != nil {
		t.Fatalf("catch-all delete: %v", err)
	}
	if err := st.DeleteAddress(ctx, catchAllID); err != store.ErrNotFound {
		t.Fatalf("double delete should be NotFound: %v", err)
	}
	t.Log("✔ catch-all delete + double-delete NotFound")
}
