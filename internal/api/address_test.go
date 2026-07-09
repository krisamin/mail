package api

import (
	"fmt"
	"testing"
)

// 주소 admin API + JIT 프로비저닝 통합 테스트 (0006 모델).

func TestAddressEndpointsAndProvision(t *testing.T) {
	srv := testServer(t)

	// 시드: 도메인 2개
	code, dom, _ := call(t, srv, "POST", "/api/admin/domain", map[string]string{"name": "krisam.in"})
	if code != 201 {
		t.Fatalf("krisam.in: %d", code)
	}
	krisamID := int64(dom["id"].(float64))
	code, dom2, _ := call(t, srv, "POST", "/api/admin/domain", map[string]string{"name": "kirby.so"})
	if code != 201 {
		t.Fatalf("kirby.so: %d", code)
	}
	kirbyID := int64(dom2["id"].(float64))

	// 1) JIT 프로비저닝 — maro 첫 로그인
	code, acc, _ := callAs(t, srv, "maro@krisam.in", "", "POST", "/api/me/provision", nil)
	if code != 200 || acc["email"] != "maro@krisam.in" {
		t.Fatalf("프로비저닝: %d %v", code, acc)
	}
	maroID := int64(acc["id"].(float64))
	// 멱등 — 재호출해도 같은 계정
	code, acc2, _ := callAs(t, srv, "maro@krisam.in", "", "POST", "/api/me/provision", nil)
	if code != 200 || int64(acc2["id"].(float64)) != maroID {
		t.Fatalf("멱등 프로비저닝: %d %v", code, acc2)
	}
	// 미등록 도메인 → 403 (로그인 게이트)
	code, _, _ = callAs(t, srv, "outsider@example.com", "", "POST", "/api/me/provision", nil)
	if code != 403 {
		t.Fatalf("미등록 도메인 프로비저닝은 403이어야: %d", code)
	}
	t.Log("✔ JIT 프로비저닝 (생성/멱등/미등록 403)")

	// 2) admin이 주소 추가 (정확 + catch-all)
	code, address, _ := call(t, srv, "POST", fmt.Sprintf("/api/admin/domain/%d/address", krisamID),
		map[string]any{"localPart": "hello", "accountId": maroID})
	if code != 201 || address["localPart"] != "hello" || address["accountEmail"] != "maro@krisam.in" {
		t.Fatalf("주소 생성: %d %v", code, address)
	}
	code, wc, _ := call(t, srv, "POST", fmt.Sprintf("/api/admin/domain/%d/address", kirbyID),
		map[string]any{"localPart": "*", "accountId": maroID})
	if code != 201 || wc["localPart"] != "*" {
		t.Fatalf("catch-all 생성: %d %v", code, wc)
	}
	t.Log("✔ 주소 생성 (정확 + catch-all, JOIN 필드 포함)")

	// 점유된 주소 → 409
	code, _, _ = call(t, srv, "POST", fmt.Sprintf("/api/admin/domain/%d/address", krisamID),
		map[string]any{"localPart": "maro", "accountId": maroID})
	if code != 409 {
		t.Fatalf("점유 주소는 409여야: %d", code)
	}
	// 중복 주소 → 409
	code, _, _ = call(t, srv, "POST", fmt.Sprintf("/api/admin/domain/%d/address", krisamID),
		map[string]any{"localPart": "hello", "accountId": maroID})
	if code != 409 {
		t.Fatalf("중복 주소는 409여야: %d", code)
	}
	// 없는 계정 → 404
	code, _, _ = call(t, srv, "POST", fmt.Sprintf("/api/admin/domain/%d/address", krisamID),
		map[string]any{"localPart": "ghost", "accountId": int64(9999)})
	if code != 404 {
		t.Fatalf("없는 계정 주소는 404여야: %d", code)
	}
	t.Log("✔ 점유/중복 409 / 없는계정 404")

	// 3) 목록 — 도메인별 (krisam.in: maro primary + hello = 2개)
	code, _, addressList := call(t, srv, "GET", fmt.Sprintf("/api/admin/domain/%d/address", krisamID), nil)
	if code != 200 || len(addressList) != 2 {
		t.Fatalf("krisam.in 주소 2개여야: %d %v", code, addressList)
	}
	// 계정별 (maro: primary + hello + catch-all = 3개)
	code, _, accountAddressList := call(t, srv, "GET", fmt.Sprintf("/api/admin/account/%d/address", maroID), nil)
	if code != 200 || len(accountAddressList) != 3 {
		t.Fatalf("maro 주소 3개여야: %d %v", code, accountAddressList)
	}
	// 본인 (/api/me/address)
	code, _, myAddressList := callAs(t, srv, "maro@krisam.in", "", "GET", "/api/me/address", nil)
	if code != 200 || len(myAddressList) != 3 {
		t.Fatalf("본인 주소 3개여야: %d %v", code, myAddressList)
	}
	t.Log("✔ 목록 (admin 도메인별/계정별 + me 본인)")

	// 4) 계정 전체 목록 (admin)
	code, _, accountList := call(t, srv, "GET", "/api/admin/account", nil)
	if code != 200 || len(accountList) != 1 || accountList[0]["email"] != "maro@krisam.in" {
		t.Fatalf("계정 목록: %d %v", code, accountList)
	}
	t.Log("✔ 계정 전체 목록")

	// 5) 일반 유저는 주소 추가 불가 (admin 전용 — 403)
	code, _, _ = callAs(t, srv, "maro@krisam.in", "", "POST",
		fmt.Sprintf("/api/admin/domain/%d/address", krisamID),
		map[string]any{"localPart": "self", "accountId": maroID})
	if code != 403 {
		t.Fatalf("일반 유저 주소 추가는 403이어야: %d", code)
	}
	t.Log("✔ 주소 추가 admin 전용 (일반 유저 403)")

	// 6) 주소 삭제 — hello 삭제 OK, primary는 catch-all 삭제 후에도 남아있어 OK,
	// 마지막 일반 주소는 400
	helloID := int64(address["id"].(float64))
	code, _, _ = call(t, srv, "DELETE", fmt.Sprintf("/api/admin/address/%d", helloID), nil)
	if code != 204 {
		t.Fatalf("삭제: %d", code)
	}
	code, _, _ = call(t, srv, "DELETE", fmt.Sprintf("/api/admin/address/%d", helloID), nil)
	if code != 404 {
		t.Fatalf("이중 삭제는 404여야: %d", code)
	}
	// primary(마지막 일반 주소) 삭제 → 400
	var primaryID int64
	_, _, accountAddressList = call(t, srv, "GET", fmt.Sprintf("/api/admin/account/%d/address", maroID), nil)
	for _, a := range accountAddressList {
		if a["localPart"] == "maro" {
			primaryID = int64(a["id"].(float64))
		}
	}
	code, _, _ = call(t, srv, "DELETE", fmt.Sprintf("/api/admin/address/%d", primaryID), nil)
	if code != 400 {
		t.Fatalf("마지막 일반 주소 삭제는 400이어야: %d", code)
	}
	t.Log("✔ 삭제 204 + 이중삭제 404 + 마지막 주소 400")
}
