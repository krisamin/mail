package api

import (
	"net/http"
	"testing"
)

// 전역 locale 설정 — 기본 auto, admin이 고정/해제, 검증 오류.
func TestLocaleSetting(t *testing.T) {
	srv := testServer(t)

	// 기본값 (미설정) = auto. 읽기는 인증 불필요.
	status, obj, _ := call(t, srv, "GET", "/api/setting/locale", nil)
	if status != http.StatusOK || obj["locale"] != "auto" {
		t.Fatalf("기본 locale: status=%d obj=%v", status, obj)
	}

	// admin이 ja로 고정.
	status, obj, _ = call(t, srv, "PUT", "/api/admin/setting/locale", map[string]string{"locale": "ja"})
	if status != http.StatusOK || obj["locale"] != "ja" {
		t.Fatalf("locale 고정: status=%d obj=%v", status, obj)
	}
	status, obj, _ = call(t, srv, "GET", "/api/setting/locale", nil)
	if status != http.StatusOK || obj["locale"] != "ja" {
		t.Fatalf("고정 후 조회: status=%d obj=%v", status, obj)
	}

	// auto로 되돌리기.
	status, obj, _ = call(t, srv, "PUT", "/api/admin/setting/locale", map[string]string{"locale": "auto"})
	if status != http.StatusOK || obj["locale"] != "auto" {
		t.Fatalf("auto 복귀: status=%d obj=%v", status, obj)
	}

	// 잘못된 값은 400.
	status, _, _ = call(t, srv, "PUT", "/api/admin/setting/locale", map[string]string{"locale": "fr"})
	if status != http.StatusBadRequest {
		t.Fatalf("잘못된 locale인데 status=%d", status)
	}
}
