package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/krisamin/mail/internal/store"
)

// 별칭 시스템 통합 테스트 (마이그레이션 0004).
// 시나리오: krisam.in + kirby.so 두 도메인, maro 유저 하나에
// 정확 별칭(hello@krisam.in)과 catch-all(*@kirby.so)을 걸고 해석 검증.

func aliasTestStore(t *testing.T) *Store {
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
		`TRUNCATE domain, account, app_password, mailbox, message, message_flag, message_blob, outbound_queue, alias RESTART IDENTITY CASCADE`)
	return st
}

func TestAlias(t *testing.T) {
	st := aliasTestStore(t)
	ctx := context.Background()

	krisam, err := st.CreateDomain(ctx, "krisam.in")
	if err != nil {
		t.Fatalf("krisam.in: %v", err)
	}
	kirby, err := st.CreateDomain(ctx, "kirby.so")
	if err != nil {
		t.Fatalf("kirby.so: %v", err)
	}
	maro, err := st.CreateAccount(ctx, krisam.ID, "maro")
	if err != nil {
		t.Fatalf("maro: %v", err)
	}
	guest, err := st.CreateAccount(ctx, krisam.ID, "guest")
	if err != nil {
		t.Fatalf("guest: %v", err)
	}

	// 1) 정확 별칭: hello@krisam.in → maro
	if _, err := st.CreateAlias(ctx, krisam.ID, "hello", maro.ID); err != nil {
		t.Fatalf("정확 별칭: %v", err)
	}
	u, err := st.ResolveAddress(ctx, "hello@krisam.in")
	if err != nil || u.ID != maro.ID {
		t.Fatalf("hello@krisam.in → maro여야: %v %+v", err, u)
	}
	t.Log("✔ 정확 별칭 해석")

	// 2) catch-all: *@kirby.so → maro
	if _, err := st.CreateAlias(ctx, kirby.ID, "*", maro.ID); err != nil {
		t.Fatalf("catch-all: %v", err)
	}
	u, err = st.ResolveAddress(ctx, "anything@kirby.so")
	if err != nil || u.ID != maro.ID {
		t.Fatalf("anything@kirby.so → maro여야: %v", err)
	}
	t.Log("✔ 와일드카드 catch-all 해석")

	// 3) 우선순위: 정확 별칭 > 와일드카드
	if _, err := st.CreateAlias(ctx, kirby.ID, "gyestt", guest.ID); err != nil {
		t.Fatalf("kirby 정확 별칭: %v", err)
	}
	u, err = st.ResolveAddress(ctx, "gyestt@kirby.so")
	if err != nil || u.ID != guest.ID {
		t.Fatalf("정확 별칭이 catch-all보다 우선이어야: %v", err)
	}
	t.Log("✔ 정확 별칭 > 와일드카드 우선순위")

	// 4) 실제 유저 > 별칭 (maro@krisam.in은 별칭 아닌 실제 유저)
	u, err = st.ResolveAddress(ctx, "maro@krisam.in")
	if err != nil || u.ID != maro.ID {
		t.Fatalf("실제 유저 해석: %v", err)
	}
	// 실제 유저 주소로 별칭 생성 시도 → 거부
	if _, err := st.CreateAlias(ctx, krisam.ID, "maro", guest.ID); err == nil {
		t.Fatal("실제 유저 주소를 별칭으로 만들 수 있으면 안 됨")
	}
	t.Log("✔ 실제 유저 주소 별칭 생성 거부")

	// 5) 별칭 없는 주소 → ErrNotFound (krisam.in엔 catch-all 없음)
	if _, err := st.ResolveAddress(ctx, "nobody@krisam.in"); err != store.ErrNotFound {
		t.Fatalf("nobody@krisam.in은 NotFound여야: %v", err)
	}
	t.Log("✔ 미등록 주소 NotFound (catch-all 없는 도메인)")

	// 6) CanSendAs
	for _, tc := range []struct {
		accountID int64
		addr   string
		want   bool
	}{
		{maro.ID, "maro@krisam.in", true},    // 본인
		{maro.ID, "hello@krisam.in", true},   // 본인 별칭
		{maro.ID, "random@kirby.so", true},   // 본인 catch-all
		{guest.ID, "hello@krisam.in", false}, // 남의 별칭
		{guest.ID, "gyestt@kirby.so", true},  // 본인 별칭 (kirby)
		{maro.ID, "gyestt@kirby.so", false},  // guest의 정확 별칭 — catch-all 있어도 정확이 우선
		{maro.ID, "x@nowhere.com", false},    // 외부
	} {
		got, err := st.CanSendAs(ctx, tc.accountID, tc.addr)
		if err != nil || got != tc.want {
			t.Fatalf("CanSendAs(%d, %s) = %v (want %v): %v", tc.accountID, tc.addr, got, tc.want, err)
		}
	}
	t.Log("✔ CanSendAs 7케이스")

	// 7) 목록/삭제
	aliasList, err := st.ListAccountAlias(ctx, maro.ID)
	if err != nil || len(aliasList) != 2 {
		t.Fatalf("maro 별칭 2개여야: %v %d", err, len(aliasList))
	}
	if aliasList[0].DomainName == "" || aliasList[0].AccountLocalPart == "" {
		t.Fatal("JOIN 편의 필드가 비어있음")
	}
	domainAliases, err := st.ListAlias(ctx, kirby.ID)
	if err != nil || len(domainAliases) != 2 {
		t.Fatalf("kirby.so 별칭 2개여야: %v %d", err, len(domainAliases))
	}
	if err := st.DeleteAlias(ctx, aliasList[0].ID); err != nil {
		t.Fatalf("삭제: %v", err)
	}
	if err := st.DeleteAlias(ctx, aliasList[0].ID); err != store.ErrNotFound {
		t.Fatalf("이중 삭제는 NotFound여야: %v", err)
	}
	t.Log("✔ 목록(JOIN 필드)/삭제/이중삭제")
}
