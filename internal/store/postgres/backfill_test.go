package postgres

import (
	"testing"

	"github.com/krisamin/mail/internal/store"
)

// TestBackfillDomainAddress: 도메인 없이 로그인한(bare) 계정이
// 도메인 추가 시점에 소급으로 주소+INBOX를 받는지.
func TestBackfillDomainAddress(t *testing.T) {
	st := addressTestStore(t)
	ctx := t.Context()

	// 1) 도메인 미등록 상태에서 두 유저 로그인 → bare 계정
	alice, err := st.ProvisionAccount(ctx, "sub-alice", "alice@late.example")
	if err != nil {
		t.Fatalf("alice: %v", err)
	}
	bob, err := st.ProvisionAccount(ctx, "sub-bob", "bob@late.example")
	if err != nil {
		t.Fatalf("bob: %v", err)
	}
	// 다른 도메인 유저 — backfill 대상 아님
	other, err := st.ProvisionAccount(ctx, "sub-other", "other@elsewhere.example")
	if err != nil {
		t.Fatalf("other: %v", err)
	}

	// 2) 도메인 추가 + backfill
	dom, err := st.CreateDomain(ctx, "late.example")
	if err != nil {
		t.Fatalf("도메인: %v", err)
	}
	created, err := st.BackfillDomainAddress(ctx, dom.ID)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if created != 2 {
		t.Fatalf("backfill 2명이어야: %d", created)
	}

	// 3) alice/bob 주소+INBOX 생김, other는 그대로
	for _, u := range []*store.Account{alice, bob} {
		addressList, err := st.ListAccountAddress(ctx, u.ID)
		if err != nil || len(addressList) != 1 {
			t.Fatalf("%s 주소 1개여야: %v %+v", u.OIDCEmail, err, addressList)
		}
		boxList, err := st.ListMailbox(ctx, u.ID)
		if err != nil || len(boxList) != 1 || boxList[0].Name != "INBOX" {
			t.Fatalf("%s INBOX 있어야: %v %+v", u.OIDCEmail, err, boxList)
		}
	}
	if addressList, _ := st.ListAccountAddress(ctx, other.ID); len(addressList) != 0 {
		t.Fatalf("other는 주소가 없어야: %+v", addressList)
	}

	// 4) 멱등 — 재실행 시 0건
	again, err := st.BackfillDomainAddress(ctx, dom.ID)
	if err != nil || again != 0 {
		t.Fatalf("재실행은 0건이어야: %v %d", err, again)
	}

	// 5) 수신 경로 확인 — backfill된 주소로 라우팅되는지
	if u, err := st.FindAccountByAddress(ctx, "alice@late.example"); err != nil || u.ID != alice.ID {
		t.Fatalf("backfill 주소 라우팅: %v", err)
	}
	t.Log("✔ 도메인 추가 소급 프로비저닝 (2명 생성, 멱등, 라우팅)")
}
