package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/krisamin/mail/internal/store"
)

// 주소 시스템 통합 테스트 (마이그레이션 0006 — account=신원, address=주소).
// 시나리오: krisam.in + kirby.so 두 도메인, maro/guest 계정에
// 주소 여러 개 + catch-all(*@kirby.so)을 걸고 해석 검증.

func addressTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("MAIL_TEST_DSN")
	if dsn == "" {
		t.Skip("MAIL_TEST_DSN 미설정 — 통합 테스트 skip")
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

	// 1) JIT 프로비저닝 — 계정 생성 + primary 주소 + INBOX
	maro, err := st.ProvisionAccount(ctx, "sub-maro", "maro@krisam.in")
	if err != nil {
		t.Fatalf("maro 프로비저닝: %v", err)
	}
	guest, err := st.ProvisionAccount(ctx, "sub-guest", "guest@krisam.in")
	if err != nil {
		t.Fatalf("guest 프로비저닝: %v", err)
	}
	boxList, err := st.ListMailbox(ctx, maro.ID)
	if err != nil || len(boxList) != 1 || boxList[0].Name != "INBOX" {
		t.Fatalf("프로비저닝 INBOX: %v %+v", err, boxList)
	}
	t.Log("✔ JIT 프로비저닝 (계정+주소+INBOX)")

	// 멱등성: 같은 sub 재호출 → 같은 계정, email 갱신
	again, err := st.ProvisionAccount(ctx, "sub-maro", "maro@krisam.in")
	if err != nil || again.ID != maro.ID {
		t.Fatalf("멱등 프로비저닝: %v %+v", err, again)
	}
	// 미등록 도메인 → ErrNotFound
	if _, err := st.ProvisionAccount(ctx, "sub-x", "x@example.com"); err != store.ErrNotFound {
		t.Fatalf("미등록 도메인은 NotFound여야: %v", err)
	}
	// 입양: 같은 email의 새 sub (IdP 유저 재생성) → 기존 계정 이어받기
	adopted, err := st.ProvisionAccount(ctx, "sub-maro-v2", "maro@krisam.in")
	if err != nil || adopted.ID != maro.ID || adopted.OIDCSubject != "sub-maro-v2" {
		t.Fatalf("입양 프로비저닝: %v %+v", err, adopted)
	}
	// 원복 (이후 케이스는 sub-maro 기준)
	if _, err := st.ProvisionAccount(ctx, "sub-maro", "maro@krisam.in"); err != nil {
		t.Fatalf("입양 원복: %v", err)
	}
	t.Log("✔ 멱등 + 미등록 도메인 거부 + 같은 email 입양")

	// 2) sub/주소 조회
	if u, err := st.FindAccountBySubject(ctx, "sub-maro"); err != nil || u.ID != maro.ID {
		t.Fatalf("FindAccountBySubject: %v", err)
	}
	if u, err := st.FindAccountByAddress(ctx, "maro@krisam.in"); err != nil || u.ID != maro.ID {
		t.Fatalf("FindAccountByAddress: %v", err)
	}
	t.Log("✔ sub/주소 조회")

	// 3) admin이 주소 추가: test@kirby.so → maro (크로스 도메인)
	if _, err := st.CreateAddress(ctx, kirby.ID, "test", maro.ID); err != nil {
		t.Fatalf("주소 추가: %v", err)
	}
	u, err := st.ResolveAddress(ctx, "test@kirby.so")
	if err != nil || u.ID != maro.ID {
		t.Fatalf("test@kirby.so → maro여야: %v", err)
	}
	t.Log("✔ admin 주소 추가 + 크로스 도메인 해석")

	// 4) catch-all: *@kirby.so → maro
	if _, err := st.CreateAddress(ctx, kirby.ID, "*", maro.ID); err != nil {
		t.Fatalf("catch-all: %v", err)
	}
	u, err = st.ResolveAddress(ctx, "anything@kirby.so")
	if err != nil || u.ID != maro.ID {
		t.Fatalf("anything@kirby.so → maro여야: %v", err)
	}
	t.Log("✔ 와일드카드 catch-all 해석")

	// 5) 우선순위: 정확 주소 > 와일드카드
	if _, err := st.CreateAddress(ctx, kirby.ID, "gyestt", guest.ID); err != nil {
		t.Fatalf("kirby 정확 주소: %v", err)
	}
	u, err = st.ResolveAddress(ctx, "gyestt@kirby.so")
	if err != nil || u.ID != guest.ID {
		t.Fatalf("정확 주소가 catch-all보다 우선이어야: %v", err)
	}
	// 점유된 주소 재생성 → duplicate
	if _, err := st.CreateAddress(ctx, kirby.ID, "gyestt", maro.ID); err == nil {
		t.Fatal("점유된 주소를 다시 만들 수 있으면 안 됨")
	}
	t.Log("✔ 정확 > 와일드카드 우선순위 + 중복 거부")

	// 6) 주소 없는 local → ErrNotFound (krisam.in엔 catch-all 없음)
	if _, err := st.ResolveAddress(ctx, "nobody@krisam.in"); err != store.ErrNotFound {
		t.Fatalf("nobody@krisam.in은 NotFound여야: %v", err)
	}
	t.Log("✔ 미등록 주소 NotFound (catch-all 없는 도메인)")

	// 7) CanSendAs
	for _, tc := range []struct {
		accountID int64
		addr      string
		want      bool
	}{
		{maro.ID, "maro@krisam.in", true},    // primary
		{maro.ID, "test@kirby.so", true},     // 추가 주소
		{maro.ID, "random@kirby.so", true},   // 본인 catch-all
		{guest.ID, "test@kirby.so", false},   // 남의 주소
		{guest.ID, "gyestt@kirby.so", true},  // 본인 주소 (kirby)
		{maro.ID, "gyestt@kirby.so", false},  // guest의 정확 주소 — catch-all 있어도 정확이 우선
		{maro.ID, "x@nowhere.com", false},    // 외부
	} {
		got, err := st.CanSendAs(ctx, tc.accountID, tc.addr)
		if err != nil || got != tc.want {
			t.Fatalf("CanSendAs(%d, %s) = %v (want %v): %v", tc.accountID, tc.addr, got, tc.want, err)
		}
	}
	t.Log("✔ CanSendAs 7케이스")

	// 8) 목록/삭제 (maro: primary + test@kirby.so + catch-all = 3개)
	addressList, err := st.ListAccountAddress(ctx, maro.ID)
	if err != nil || len(addressList) != 3 {
		t.Fatalf("maro 주소 3개여야: %v %d", err, len(addressList))
	}
	if addressList[0].DomainName == "" || addressList[0].AccountEmail == "" {
		t.Fatal("JOIN 편의 필드가 비어있음")
	}
	domainAddressList, err := st.ListAddress(ctx, kirby.ID)
	if err != nil || len(domainAddressList) != 3 {
		t.Fatalf("kirby.so 주소 3개여야: %v %d", err, len(domainAddressList))
	}

	// 마지막 일반 주소 삭제는 거부 — guest는 gyestt 지우면 primary만 남음.
	// guest의 primary(guest@krisam.in)를 지우려면 gyestt가 있어 OK,
	// 그 다음 gyestt(마지막)는 거부돼야 한다.
	var guestPrimaryID int64
	guestAddressList, _ := st.ListAccountAddress(ctx, guest.ID)
	for _, a := range guestAddressList {
		if a.DomainName == "krisam.in" {
			guestPrimaryID = a.ID
		}
	}
	if err := st.DeleteAddress(ctx, guestPrimaryID); err != nil {
		t.Fatalf("guest primary 삭제 (다른 주소 있어 허용): %v", err)
	}
	guestAddressList, _ = st.ListAccountAddress(ctx, guest.ID)
	if len(guestAddressList) != 1 {
		t.Fatalf("guest 주소 1개 남아야: %d", len(guestAddressList))
	}
	if err := st.DeleteAddress(ctx, guestAddressList[0].ID); err == nil {
		t.Fatal("마지막 일반 주소 삭제가 성공하면 안 됨")
	}
	t.Log("✔ 목록(JOIN 필드) + 마지막 주소 삭제 방어")

	// catch-all은 마지막이어도 삭제 가능 (일반 주소 아님)
	var catchAllID int64
	maroAddressList, _ := st.ListAccountAddress(ctx, maro.ID)
	for _, a := range maroAddressList {
		if a.LocalPart == "*" {
			catchAllID = a.ID
		}
	}
	if err := st.DeleteAddress(ctx, catchAllID); err != nil {
		t.Fatalf("catch-all 삭제: %v", err)
	}
	if err := st.DeleteAddress(ctx, catchAllID); err != store.ErrNotFound {
		t.Fatalf("이중 삭제는 NotFound여야: %v", err)
	}
	t.Log("✔ catch-all 삭제 + 이중삭제 NotFound")
}
