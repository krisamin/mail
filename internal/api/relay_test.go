package api

import (
	"fmt"
	"testing"
)

// relay admin API 통합 테스트 — CRUD + password 비노출 + 도메인 지정.

func TestRelayEndpoints(t *testing.T) {
	srv := testServer(t)

	// 시드: 도메인
	code, dom, _ := call(t, srv, "POST", "/api/admin/domain", map[string]string{"name": "krisam.in"})
	if code != 201 {
		t.Fatalf("도메인: %d", code)
	}
	domID := int64(dom["id"].(float64))

	// 1) 생성 (password 포함) → 응답에 password 없음 + hasPassword=true
	code, rl, _ := call(t, srv, "POST", "/api/admin/relay", map[string]any{
		"name": "resend", "host": "smtp.resend.com", "port": 587,
		"username": "resend", "password": "re_secret", "isDefault": true,
	})
	if code != 201 || rl["name"] != "resend" {
		t.Fatalf("relay 생성: %d %v", code, rl)
	}
	if _, leaked := rl["password"]; leaked {
		t.Fatalf("★password가 응답에 노출됨: %v", rl)
	}
	if rl["hasPassword"] != true {
		t.Fatalf("hasPassword=true여야: %v", rl)
	}
	relayID := int64(rl["id"].(float64))
	t.Log("✔ relay 생성 + password 비노출 (hasPassword만)")

	// 2) 목록에도 password 없음
	code, _, listArr := call(t, srv, "GET", "/api/admin/relay", nil)
	if code != 200 || len(listArr) != 1 {
		t.Fatalf("relay 목록: %d %v", code, listArr)
	}
	if _, leaked := listArr[0]["password"]; leaked {
		t.Fatalf("★목록에 password 노출: %v", listArr[0])
	}
	t.Log("✔ 목록 password 비노출")

	// 3) 수정 — password 빈 문자열 = 유지 (hasPassword 여전히 true)
	code, updated, _ := call(t, srv, "PUT", fmt.Sprintf("/api/admin/relay/%d", relayID),
		map[string]any{"name": "resend", "host": "smtp2.resend.com", "port": 587,
			"username": "resend", "password": "", "isDefault": true})
	if code != 200 || updated["host"] != "smtp2.resend.com" || updated["hasPassword"] != true {
		t.Fatalf("relay 수정: %d %v", code, updated)
	}
	t.Log("✔ 수정 시 password 빈 문자열 = 기존 유지")

	// 4) 도메인 relay 지정 + 해제
	code, _, _ = call(t, srv, "PUT", fmt.Sprintf("/api/admin/domain/%d/relay", domID),
		map[string]any{"relayId": relayID})
	if code != 200 {
		t.Fatalf("도메인 relay 지정: %d", code)
	}
	code, _, _ = call(t, srv, "PUT", fmt.Sprintf("/api/admin/domain/%d/relay", domID),
		map[string]any{"relayId": nil})
	if code != 200 {
		t.Fatalf("도메인 relay 해제: %d", code)
	}
	t.Log("✔ 도메인 relay 지정/해제")

	// 5) 삭제
	code, _, _ = call(t, srv, "DELETE", fmt.Sprintf("/api/admin/relay/%d", relayID), nil)
	if code != 204 {
		t.Fatalf("relay 삭제: %d", code)
	}
	code, _, _ = call(t, srv, "DELETE", fmt.Sprintf("/api/admin/relay/%d", relayID), nil)
	if code != 404 {
		t.Fatalf("없는 relay 삭제는 404여야: %d", code)
	}
	t.Log("✔ relay 삭제 + 404")
}
