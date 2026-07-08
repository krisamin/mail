package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 셀프서비스(/api/me/*) 통합 테스트.
// InsecureSkipVerify 모드에서 X-Test-Email/X-Test-Groups 헤더로
// "누가 로그인했는가"를 흉내낸다 (auth.go authenticate 참고).

// callAs는 지정한 신원으로 JSON 요청을 보낸다.
func callAs(t *testing.T, srv *httptest.Server, email, groups, method, path string, body any) (int, map[string]any, []map[string]any) {
	t.Helper()
	var reqBody *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reqBody = bytes.NewReader(b)
	} else {
		reqBody = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, srv.URL+path, reqBody)
	if err != nil {
		t.Fatalf("req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Email", email)
	// 빈 문자열이라도 명시하면 그룹 override (기본 admin 방지)
	req.Header["X-Test-Groups"] = []string{groups}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	raw := buf.Bytes()
	var obj map[string]any
	var arr []map[string]any
	if len(raw) > 0 {
		if raw[0] == '[' {
			_ = json.Unmarshal(raw, &arr)
		} else {
			_ = json.Unmarshal(raw, &obj)
		}
	}
	return resp.StatusCode, obj, arr
}

// TestSelfService: 일반 유저(그룹 없음)의 본인 앱비번 라이프사이클 + 경계.
func TestSelfService(t *testing.T) {
	srv := testServer(t)

	// 시드: admin 권한으로 도메인 + 유저 2명 (maro, guest)
	code, dom, _ := call(t, srv, "POST", "/api/admin/domain", map[string]string{"name": "krisam.in"})
	if code != 201 {
		t.Fatalf("도메인: %d %v", code, dom)
	}
	domID := int64(dom["id"].(float64))
	for _, name := range []string{"maro", "guest"} {
		if code, u, _ := call(t, srv, "POST", fmt.Sprintf("/api/admin/domain/%d/account", domID),
			map[string]string{"localPart": name}); code != 201 {
			t.Fatalf("유저 %s: %d %v", name, code, u)
		}
	}

	// 1) 일반 유저는 admin API 접근 불가 (403)
	code, _, _ = callAs(t, srv, "guest@krisam.in", "", "GET", "/api/admin/domain", nil)
	if code != 403 {
		t.Fatalf("일반 유저의 admin 접근은 403이어야: %d", code)
	}
	t.Log("✔ 일반 유저 admin API 403")

	// 2) 본인 계정 조회
	code, acc, _ := callAs(t, srv, "guest@krisam.in", "", "GET", "/api/me/account", nil)
	if code != 200 || acc["localPart"] != "guest" {
		t.Fatalf("본인 계정: %d %v", code, acc)
	}
	t.Log("✔ /api/me/account — email 클레임 → 메일 계정 매핑")

	// 메일 계정 없는 유저 → 404
	code, _, _ = callAs(t, srv, "nobody@krisam.in", "", "GET", "/api/me/account", nil)
	if code != 404 {
		t.Fatalf("계정 없는 유저는 404여야: %d", code)
	}
	t.Log("✔ 미개설 계정 404")

	// 3) 본인 앱비번 발급 → 목록 → revoke
	code, pw, _ := callAs(t, srv, "guest@krisam.in", "", "POST", "/api/me/app-password",
		map[string]string{"label": "내 폰"})
	if code != 201 || pw["plaintext"] == nil {
		t.Fatalf("발급: %d %v", code, pw)
	}
	guestPwID := int64(pw["appPassword"].(map[string]any)["id"].(float64))
	t.Logf("✔ 본인 앱비번 발급: %v", pw["plaintext"])

	code, _, passwordList := callAs(t, srv, "guest@krisam.in", "", "GET", "/api/me/app-password", nil)
	if code != 200 || len(passwordList) != 1 {
		t.Fatalf("목록: %d %v", code, passwordList)
	}

	// 4) IDOR 방지 — maro가 guest의 비번을 revoke 시도 → 404
	code, _, _ = callAs(t, srv, "maro@krisam.in", "", "DELETE",
		fmt.Sprintf("/api/me/app-password/%d", guestPwID), nil)
	if code != 404 {
		t.Fatalf("타인 비번 revoke는 404여야 (IDOR): %d", code)
	}
	t.Log("✔ IDOR 방지 — 타인 앱비번 revoke 404")

	// 본인 revoke는 성공
	code, _, _ = callAs(t, srv, "guest@krisam.in", "", "DELETE",
		fmt.Sprintf("/api/me/app-password/%d", guestPwID), nil)
	if code != 204 {
		t.Fatalf("본인 revoke: %d", code)
	}
	code, _, passwordList = callAs(t, srv, "guest@krisam.in", "", "GET", "/api/me/app-password", nil)
	if code != 200 || len(passwordList) != 1 || passwordList[0]["revoked"] != true {
		t.Fatalf("revoke 반영: %d %v", code, passwordList)
	}
	t.Log("✔ 본인 revoke 204 + 반영")

	// 5) 대소문자 이메일 정규화 (Guest@Krisam.IN → guest)
	code, acc, _ = callAs(t, srv, "Guest@Krisam.IN", "", "GET", "/api/me/account", nil)
	if code != 200 || acc["localPart"] != "guest" {
		t.Fatalf("대소문자 정규화: %d %v", code, acc)
	}
	t.Log("✔ email 대소문자 정규화")
}
