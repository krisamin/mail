package api

import (
	"fmt"
	"testing"
)

// Address admin API + JIT provisioning integration tests (0006 model).

func TestAddressEndpointsAndProvision(t *testing.T) {
	srv := testServer(t)

	// seed: two domains
	code, dom, _ := call(t, srv, "POST", "/api/admin/domain", map[string]string{"name": "krisam.in"})
	if code != 201 {
		t.Fatalf("krisam.in: %d", code)
	}
	krisamID := dom["id"].(string)
	code, dom2, _ := call(t, srv, "POST", "/api/admin/domain", map[string]string{"name": "kirby.so"})
	if code != 201 {
		t.Fatalf("kirby.so: %d", code)
	}
	kirbyID := dom2["id"].(string)

	// 1) JIT provisioning — maro's first login
	code, acc, _ := callAs(t, srv, "maro@krisam.in", "", "POST", "/api/me/provision", nil)
	if code != 200 || acc["email"] != "maro@krisam.in" {
		t.Fatalf("provisioning: %d %v", code, acc)
	}
	maroID := acc["id"].(string)
	// idempotent — calling again returns the same account
	code, acc2, _ := callAs(t, srv, "maro@krisam.in", "", "POST", "/api/me/provision", nil)
	if code != 200 || acc2["id"].(string) != maroID {
		t.Fatalf("idempotent provisioning: %d %v", code, acc2)
	}
	// unregistered domain → 200 bare account (login allowed, just no address)
	code, bare, _ := callAs(t, srv, "outsider@example.com", "", "POST", "/api/me/provision", nil)
	if code != 200 || bare["email"] != "outsider@example.com" {
		t.Fatalf("unregistered domain should still allow login: %d %v", code, bare)
	}
	t.Log("✔ JIT provisioning (create/idempotent/bare account even for unregistered)")

	// 2) admin adds addresses (exact + catch-all)
	code, address, _ := call(t, srv, "POST", fmt.Sprintf("/api/admin/domain/%s/address", krisamID),
		map[string]any{"localPart": "hello", "accountId": maroID})
	if code != 201 || address["localPart"] != "hello" || address["accountEmail"] != "maro@krisam.in" {
		t.Fatalf("create address: %d %v", code, address)
	}
	code, wc, _ := call(t, srv, "POST", fmt.Sprintf("/api/admin/domain/%s/address", kirbyID),
		map[string]any{"localPart": "*", "accountId": maroID})
	if code != 201 || wc["localPart"] != "*" {
		t.Fatalf("create catch-all: %d %v", code, wc)
	}
	t.Log("✔ address creation (exact + catch-all, JOIN fields included)")

	// occupied address → 409
	code, _, _ = call(t, srv, "POST", fmt.Sprintf("/api/admin/domain/%s/address", krisamID),
		map[string]any{"localPart": "maro", "accountId": maroID})
	if code != 409 {
		t.Fatalf("occupied address should be 409: %d", code)
	}
	// duplicate address → 409
	code, _, _ = call(t, srv, "POST", fmt.Sprintf("/api/admin/domain/%s/address", krisamID),
		map[string]any{"localPart": "hello", "accountId": maroID})
	if code != 409 {
		t.Fatalf("duplicate address should be 409: %d", code)
	}
	// nonexistent account → 404
	code, _, _ = call(t, srv, "POST", fmt.Sprintf("/api/admin/domain/%s/address", krisamID),
		map[string]any{"localPart": "ghost", "accountId": "00000000-0000-0000-0000-000000009999"})
	if code != 404 {
		t.Fatalf("address for nonexistent account should be 404: %d", code)
	}
	t.Log("✔ occupied/duplicate 409 / nonexistent account 404")

	// 3) Lists — per domain (krisam.in: maro primary + hello = 2)
	code, _, addressList := call(t, srv, "GET", fmt.Sprintf("/api/admin/domain/%s/address", krisamID), nil)
	if code != 200 || len(addressList) != 2 {
		t.Fatalf("krisam.in should have 2 addresses: %d %v", code, addressList)
	}
	// per account (maro: primary + hello + catch-all = 3)
	code, _, accountAddressList := call(t, srv, "GET", fmt.Sprintf("/api/admin/account/%s/address", maroID), nil)
	if code != 200 || len(accountAddressList) != 3 {
		t.Fatalf("maro should have 3 addresses: %d %v", code, accountAddressList)
	}
	// self (/api/me/address)
	code, _, myAddressList := callAs(t, srv, "maro@krisam.in", "", "GET", "/api/me/address", nil)
	if code != 200 || len(myAddressList) != 3 {
		t.Fatalf("self should have 3 addresses: %d %v", code, myAddressList)
	}
	t.Log("✔ lists (admin per-domain/per-account + me self)")

	// 4) Full account list (admin) — maro + outsider(bare) = 2
	code, _, accountList := call(t, srv, "GET", "/api/admin/account", nil)
	if code != 200 || len(accountList) != 2 {
		t.Fatalf("account list: %d %v", code, accountList)
	}
	t.Log("✔ full account list")

	// 5) Regular users cannot add addresses (admin only — 403)
	code, _, _ = callAs(t, srv, "maro@krisam.in", "", "POST",
		fmt.Sprintf("/api/admin/domain/%s/address", krisamID),
		map[string]any{"localPart": "self", "accountId": maroID})
	if code != 403 {
		t.Fatalf("address creation by regular user should be 403: %d", code)
	}
	t.Log("✔ address creation admin-only (regular user 403)")

	// 5.5) Account-based address creation — [local]@[choose domain] UX path
	code, byAccount, _ := call(t, srv, "POST", fmt.Sprintf("/api/admin/account/%s/address", maroID),
		map[string]any{"localPart": "second", "domainId": kirbyID})
	if code != 201 || byAccount["localPart"] != "second" || byAccount["domainName"] != "kirby.so" {
		t.Fatalf("account-based address creation: %d %v", code, byAccount)
	}
	code, _, _ = call(t, srv, "DELETE", fmt.Sprintf("/api/admin/address/%v", byAccount["id"]), nil)
	if code != 204 {
		t.Fatalf("account-based address deletion: %d", code)
	}
	t.Log("✔ account-based address creation (POST /account/{id}/address)")

	// 5.7) Service account — no login, only address + app password
	code, svc, _ := call(t, srv, "POST", "/api/admin/account/service",
		map[string]string{"email": "bot@kirby.so"})
	if code != 201 || svc["kind"] != "service" || svc["email"] != "bot@kirby.so" {
		t.Fatalf("create service account: %d %v", code, svc)
	}
	svcID := svc["id"].(string)
	// unregistered domain → 400
	code, _, _ = call(t, srv, "POST", "/api/admin/account/service",
		map[string]string{"email": "bot@example.com"})
	if code != 400 {
		t.Fatalf("service account on unregistered domain should be 400: %d", code)
	}
	// occupied address → 409
	code, _, _ = call(t, srv, "POST", "/api/admin/account/service",
		map[string]string{"email": "maro@krisam.in"})
	if code != 409 {
		t.Fatalf("service account with occupied address should be 409: %d", code)
	}
	// OIDC login with the same email must not adopt/log into the service account (hijack prevention)
	code, _, _ = callAs(t, srv, "bot@kirby.so", "", "POST", "/api/me/provision", nil)
	if code == 200 {
		t.Fatal("provisioning with a service account email must not succeed (hijack)")
	}
	// app passwords can be issued for service accounts too
	code, svcPw, _ := call(t, srv, "POST", fmt.Sprintf("/api/admin/account/%s/app-password", svcID),
		map[string]string{"label": "bot-smtp"})
	if code != 201 || svcPw["plaintext"] == nil {
		t.Fatalf("service account app password: %d %v", code, svcPw)
	}
	t.Log("✔ service account (create/unregistered 400/occupied 409/hijack prevention/app password)")

	// 6) Address deletion — deleting hello OK, primary still remains after catch-all deletion OK,
	// deleting the last regular address is 400
	helloID := address["id"].(string)
	code, _, _ = call(t, srv, "DELETE", fmt.Sprintf("/api/admin/address/%s", helloID), nil)
	if code != 204 {
		t.Fatalf("delete: %d", code)
	}
	code, _, _ = call(t, srv, "DELETE", fmt.Sprintf("/api/admin/address/%s", helloID), nil)
	if code != 404 {
		t.Fatalf("double delete should be 404: %d", code)
	}
	// deleting primary (the last regular address) → 400
	var primaryID string
	_, _, accountAddressList = call(t, srv, "GET", fmt.Sprintf("/api/admin/account/%s/address", maroID), nil)
	for _, a := range accountAddressList {
		if a["localPart"] == "maro" {
			primaryID = a["id"].(string)
		}
	}
	code, _, _ = call(t, srv, "DELETE", fmt.Sprintf("/api/admin/address/%s", primaryID), nil)
	if code != 400 {
		t.Fatalf("deleting the last regular address should be 400: %d", code)
	}
	t.Log("✔ delete 204 + double delete 404 + last address 400")
}
