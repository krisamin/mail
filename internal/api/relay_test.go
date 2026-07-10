package api

import (
	"fmt"
	"testing"
)

// relay admin API integration tests — CRUD + password not exposed + domain assignment.

func TestRelayEndpoints(t *testing.T) {
	srv := testServer(t)

	// seed: domain
	code, dom, _ := call(t, srv, "POST", "/api/admin/domain", map[string]string{"name": "krisam.in"})
	if code != 201 {
		t.Fatalf("domain: %d", code)
	}
	domID := int64(dom["id"].(float64))

	// 1) Create (with password) → response has no password + hasPassword=true
	code, rl, _ := call(t, srv, "POST", "/api/admin/relay", map[string]any{
		"name": "resend", "host": "smtp.resend.com", "port": 587,
		"username": "resend", "password": "re_secret", "isDefault": true,
	})
	if code != 201 || rl["name"] != "resend" {
		t.Fatalf("create relay: %d %v", code, rl)
	}
	if _, leaked := rl["password"]; leaked {
		t.Fatalf("★password exposed in response: %v", rl)
	}
	if rl["hasPassword"] != true {
		t.Fatalf("hasPassword should be true: %v", rl)
	}
	relayID := int64(rl["id"].(float64))
	t.Log("✔ relay created + password not exposed (hasPassword only)")

	// 2) List also has no password
	code, _, listArr := call(t, srv, "GET", "/api/admin/relay", nil)
	if code != 200 || len(listArr) != 1 {
		t.Fatalf("relay list: %d %v", code, listArr)
	}
	if _, leaked := listArr[0]["password"]; leaked {
		t.Fatalf("★password exposed in list: %v", listArr[0])
	}
	t.Log("✔ list does not expose password")

	// 3) Update — empty password string = keep (hasPassword still true)
	code, updated, _ := call(t, srv, "PUT", fmt.Sprintf("/api/admin/relay/%d", relayID),
		map[string]any{"name": "resend", "host": "smtp2.resend.com", "port": 587,
			"username": "resend", "password": "", "isDefault": true})
	if code != 200 || updated["host"] != "smtp2.resend.com" || updated["hasPassword"] != true {
		t.Fatalf("update relay: %d %v", code, updated)
	}
	t.Log("✔ empty password string on update = keep existing")

	// 4) Assign + unassign domain relay
	code, _, _ = call(t, srv, "PUT", fmt.Sprintf("/api/admin/domain/%d/relay", domID),
		map[string]any{"relayId": relayID})
	if code != 200 {
		t.Fatalf("assign domain relay: %d", code)
	}
	code, _, _ = call(t, srv, "PUT", fmt.Sprintf("/api/admin/domain/%d/relay", domID),
		map[string]any{"relayId": nil})
	if code != 200 {
		t.Fatalf("unassign domain relay: %d", code)
	}
	t.Log("✔ domain relay assign/unassign")

	// 5) Delete
	code, _, _ = call(t, srv, "DELETE", fmt.Sprintf("/api/admin/relay/%d", relayID), nil)
	if code != 204 {
		t.Fatalf("delete relay: %d", code)
	}
	code, _, _ = call(t, srv, "DELETE", fmt.Sprintf("/api/admin/relay/%d", relayID), nil)
	if code != 404 {
		t.Fatalf("deleting a nonexistent relay should be 404: %d", code)
	}
	t.Log("✔ relay delete + 404")
}
