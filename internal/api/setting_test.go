package api

import (
	"net/http"
	"testing"
)

// Global locale setting — default auto, admin pins/unpins, validation errors.
func TestLocaleSetting(t *testing.T) {
	srv := testServer(t)

	// Default (unset) = auto. Reading requires no auth.
	status, obj, _ := call(t, srv, "GET", "/api/setting/locale", nil)
	if status != http.StatusOK || obj["locale"] != "auto" {
		t.Fatalf("default locale: status=%d obj=%v", status, obj)
	}

	// admin pins it to ja.
	status, obj, _ = call(t, srv, "PUT", "/api/admin/setting/locale", map[string]string{"locale": "ja"})
	if status != http.StatusOK || obj["locale"] != "ja" {
		t.Fatalf("pin locale: status=%d obj=%v", status, obj)
	}
	status, obj, _ = call(t, srv, "GET", "/api/setting/locale", nil)
	if status != http.StatusOK || obj["locale"] != "ja" {
		t.Fatalf("read after pin: status=%d obj=%v", status, obj)
	}

	// Revert to auto.
	status, obj, _ = call(t, srv, "PUT", "/api/admin/setting/locale", map[string]string{"locale": "auto"})
	if status != http.StatusOK || obj["locale"] != "auto" {
		t.Fatalf("revert to auto: status=%d obj=%v", status, obj)
	}

	// Invalid value is 400.
	status, _, _ = call(t, srv, "PUT", "/api/admin/setting/locale", map[string]string{"locale": "fr"})
	if status != http.StatusBadRequest {
		t.Fatalf("invalid locale but status=%d", status)
	}
}
