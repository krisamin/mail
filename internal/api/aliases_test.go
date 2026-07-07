package api

import (
	"fmt"
	"testing"
)

// 별칭 admin API + 로그인 게이트 통합 테스트.

func TestAliasEndpointsAndGate(t *testing.T) {
	srv := testServer(t)

	// 시드: 도메인 2개 + 유저
	code, dom, _ := call(t, srv, "POST", "/api/admin/domains", map[string]string{"name": "krisam.in"})
	if code != 201 {
		t.Fatalf("krisam.in: %d", code)
	}
	krisamID := int64(dom["id"].(float64))
	code, dom2, _ := call(t, srv, "POST", "/api/admin/domains", map[string]string{"name": "kirby.so"})
	if code != 201 {
		t.Fatalf("kirby.so: %d", code)
	}
	kirbyID := int64(dom2["id"].(float64))
	code, u, _ := call(t, srv, "POST", fmt.Sprintf("/api/admin/domains/%d/users", krisamID),
		map[string]string{"localPart": "maro"})
	if code != 201 {
		t.Fatalf("maro: %d", code)
	}
	maroID := int64(u["id"].(float64))

	// 1) 별칭 생성 (정확 + catch-all)
	code, alias, _ := call(t, srv, "POST", fmt.Sprintf("/api/admin/domains/%d/aliases", krisamID),
		map[string]any{"localPart": "hello", "userId": maroID})
	if code != 201 || alias["localPart"] != "hello" || alias["userLocalPart"] != "maro" {
		t.Fatalf("별칭 생성: %d %v", code, alias)
	}
	code, wc, _ := call(t, srv, "POST", fmt.Sprintf("/api/admin/domains/%d/aliases", kirbyID),
		map[string]any{"localPart": "*", "userId": maroID})
	if code != 201 || wc["localPart"] != "*" {
		t.Fatalf("catch-all 생성: %d %v", code, wc)
	}
	t.Log("✔ 별칭 생성 (정확 + catch-all, JOIN 필드 포함)")

	// 실제 유저 주소로 별칭 → 400
	code, _, _ = call(t, srv, "POST", fmt.Sprintf("/api/admin/domains/%d/aliases", krisamID),
		map[string]any{"localPart": "maro", "userId": maroID})
	if code != 400 {
		t.Fatalf("유저 주소 별칭은 400이어야: %d", code)
	}
	// 중복 별칭 → 409
	code, _, _ = call(t, srv, "POST", fmt.Sprintf("/api/admin/domains/%d/aliases", krisamID),
		map[string]any{"localPart": "hello", "userId": maroID})
	if code != 409 {
		t.Fatalf("중복 별칭은 409여야: %d", code)
	}
	// 없는 유저 → 404
	code, _, _ = call(t, srv, "POST", fmt.Sprintf("/api/admin/domains/%d/aliases", krisamID),
		map[string]any{"localPart": "ghost", "userId": int64(9999)})
	if code != 404 {
		t.Fatalf("없는 유저 별칭은 404여야: %d", code)
	}
	t.Log("✔ 유저주소 400 / 중복 409 / 없는유저 404")

	// 2) 도메인 별칭 목록
	code, _, aliases := call(t, srv, "GET", fmt.Sprintf("/api/admin/domains/%d/aliases", krisamID), nil)
	if code != 200 || len(aliases) != 1 {
		t.Fatalf("krisam.in 별칭 1개여야: %d %v", code, aliases)
	}

	// 3) /api/me/aliases — maro 본인 별칭 (양 도메인 합산 2개)
	code, _, myAliases := callAs(t, srv, "maro@krisam.in", "", "GET", "/api/me/aliases", nil)
	if code != 200 || len(myAliases) != 2 {
		t.Fatalf("본인 별칭 2개여야: %d %v", code, myAliases)
	}
	t.Log("✔ 목록 (admin 도메인별 + me 본인)")

	// 4) 로그인 게이트
	for _, tc := range []struct {
		email                 string
		wantDomain, wantAccnt bool
	}{
		{"maro@krisam.in", true, true},      // 도메인 O, 계정 O
		{"newbie@krisam.in", true, false},   // 도메인 O, 계정 X → 로그인 허용(미개설 안내)
		{"anyone@kirby.so", true, false},    // 도메인 O (catch-all 있어도 계정은 아님)
		{"outsider@example.com", false, false}, // 도메인 X → 로그인 거부 대상
	} {
		code, gate, _ := callAs(t, srv, tc.email, "", "GET", "/api/me/gate", nil)
		if code != 200 || gate["domainExists"] != tc.wantDomain || gate["accountExists"] != tc.wantAccnt {
			t.Fatalf("gate(%s) = %d %v (want domain=%v account=%v)",
				tc.email, code, gate, tc.wantDomain, tc.wantAccnt)
		}
	}
	t.Log("✔ 로그인 게이트 4케이스 (도메인 유/무 × 계정 유/무)")

	// 5) 별칭 삭제
	aliasID := int64(alias["id"].(float64))
	code, _, _ = call(t, srv, "DELETE", fmt.Sprintf("/api/admin/aliases/%d", aliasID), nil)
	if code != 204 {
		t.Fatalf("삭제: %d", code)
	}
	code, _, _ = call(t, srv, "DELETE", fmt.Sprintf("/api/admin/aliases/%d", aliasID), nil)
	if code != 404 {
		t.Fatalf("이중 삭제는 404여야: %d", code)
	}
	t.Log("✔ 삭제 204 + 이중삭제 404")
}
