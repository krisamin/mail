package api

import (
	"fmt"
	"testing"
)

// Filter rule API integration tests — CRUD + reorder + the delivery-path
// application (webmail local send runs the same filter.Evaluate hook as SMTP).

func TestFilterRule(t *testing.T) {
	srv := testServer(t)

	if code, _, _ := call(t, srv, "POST", "/api/admin/domain", map[string]string{"name": "krisam.in"}); code != 201 {
		t.Fatalf("domain")
	}
	for _, name := range []string{"maro", "guest"} {
		if code, _, _ := callAs(t, srv, name+"@krisam.in", "", "POST", "/api/me/provision", nil); code != 200 {
			t.Fatalf("user %s", name)
		}
	}

	// 1) create: newsletter → move to Letters
	code, rule1, _ := callAs(t, srv, "maro@krisam.in", "", "POST", "/api/me/filter", map[string]any{
		"name": "newsletters", "field": "from", "matchType": "contains",
		"pattern": "@shop.example", "action": "move", "actionMailbox": "Letters",
	})
	if code != 201 || rule1["position"].(float64) != 1 {
		t.Fatalf("create rule1: %d %v", code, rule1)
	}
	rule1ID := rule1["id"].(string)

	// invalid enum → 400
	code, _, _ = callAs(t, srv, "maro@krisam.in", "", "POST", "/api/me/filter", map[string]any{
		"name": "bad", "field": "nope", "matchType": "contains",
		"pattern": "x", "action": "move", "actionMailbox": "X",
	})
	if code != 400 {
		t.Fatalf("invalid field should be 400: %d", code)
	}
	// move without mailbox → 400
	code, _, _ = callAs(t, srv, "maro@krisam.in", "", "POST", "/api/me/filter", map[string]any{
		"name": "bad", "field": "from", "matchType": "contains",
		"pattern": "x", "action": "move",
	})
	if code != 400 {
		t.Fatalf("move without mailbox should be 400: %d", code)
	}
	t.Log("✔ create + validation")

	// 2) second rule: subject contains "spam-ish" → discard
	code, rule2, _ := callAs(t, srv, "maro@krisam.in", "", "POST", "/api/me/filter", map[string]any{
		"name": "drop", "field": "subject", "matchType": "contains",
		"pattern": "buy now", "action": "discard",
	})
	if code != 201 || rule2["position"].(float64) != 2 {
		t.Fatalf("create rule2: %d %v", code, rule2)
	}
	rule2ID := rule2["id"].(string)

	// 3) reorder — rule2 up, now first
	code, _, _ = callAs(t, srv, "maro@krisam.in", "", "POST",
		fmt.Sprintf("/api/me/filter/%s/move", rule2ID), map[string]int{"direction": -1})
	if code != 204 {
		t.Fatalf("move up: %d", code)
	}
	code, _, ruleList := callAs(t, srv, "maro@krisam.in", "", "GET", "/api/me/filter", nil)
	if code != 200 || len(ruleList) != 2 || ruleList[0]["id"].(string) != rule2ID {
		t.Fatalf("order after move: %v", ruleList)
	}
	// up again at the edge — no-op, still 204
	code, _, _ = callAs(t, srv, "maro@krisam.in", "", "POST",
		fmt.Sprintf("/api/me/filter/%s/move", rule2ID), map[string]int{"direction": -1})
	if code != 204 {
		t.Fatalf("edge move: %d", code)
	}
	t.Log("✔ reorder + edge no-op")

	// 4) cross-account isolation — guest cannot touch maro's rule
	code, _, _ = callAs(t, srv, "guest@krisam.in", "", "DELETE",
		fmt.Sprintf("/api/me/filter/%s", rule1ID), nil)
	if code != 404 {
		t.Fatalf("cross-account delete should be 404: %d", code)
	}
	code, _, guestRuleList := callAs(t, srv, "guest@krisam.in", "", "GET", "/api/me/filter", nil)
	if code != 200 || len(guestRuleList) != 0 {
		t.Fatalf("guest rule list should be empty: %v", guestRuleList)
	}
	t.Log("✔ cross-account isolation")

	// 5) delivery path — guest sends to maro; the "move to Letters" rule
	// applies (from matches @krisam.in? no — pattern is @shop.example).
	// Use a subject rule instead for a real end-to-end assertion.
	code, _, _ = callAs(t, srv, "maro@krisam.in", "", "POST", "/api/me/filter", map[string]any{
		"name": "receipts", "field": "subject", "matchType": "prefix",
		"pattern": "[receipt]", "action": "move", "actionMailbox": "Receipts",
	})
	if code != 201 {
		t.Fatalf("create receipt rule: %d", code)
	}
	code, _, _ = callAs(t, srv, "guest@krisam.in", "", "POST", "/api/me/send", map[string]any{
		"from": "guest@krisam.in", "toList": []string{"maro@krisam.in"},
		"subject": "[receipt] order 42", "textBody": "total 12000",
	})
	if code != 200 {
		t.Fatalf("send: %d", code)
	}
	code, page, _ := callAs(t, srv, "maro@krisam.in", "", "GET", "/api/me/message?mailbox=Receipts", nil)
	if code != 200 || len(page["messageList"].([]any)) != 1 {
		t.Fatalf("Receipts should hold the message: %d %v", code, page)
	}
	code, page, _ = callAs(t, srv, "maro@krisam.in", "", "GET", "/api/me/message?mailbox=INBOX", nil)
	if code != 200 || len(page["messageList"].([]any)) != 0 {
		t.Fatalf("INBOX should be empty: %v", page)
	}
	t.Log("✔ delivery path — move rule routes to Receipts")

	// 6) discard rule end-to-end
	code, _, _ = callAs(t, srv, "guest@krisam.in", "", "POST", "/api/me/send", map[string]any{
		"from": "guest@krisam.in", "toList": []string{"maro@krisam.in"},
		"subject": "please BUY NOW cheap", "textBody": "spam",
	})
	if code != 200 {
		t.Fatalf("discard send: %d", code)
	}
	code, page, _ = callAs(t, srv, "maro@krisam.in", "", "GET", "/api/me/message?mailbox=INBOX", nil)
	if code != 200 || len(page["messageList"].([]any)) != 0 {
		t.Fatalf("discarded mail must not appear: %v", page)
	}
	t.Log("✔ delivery path — discard drops silently (case-insensitive)")

	// 7) markSeen rule
	code, _, _ = callAs(t, srv, "maro@krisam.in", "", "POST", "/api/me/filter", map[string]any{
		"name": "auto-read", "field": "from", "matchType": "equals",
		"pattern": "guest@krisam.in", "action": "markSeen",
	})
	if code != 201 {
		t.Fatalf("markSeen rule: %d", code)
	}
	code, _, _ = callAs(t, srv, "guest@krisam.in", "", "POST", "/api/me/send", map[string]any{
		"from": "guest@krisam.in", "toList": []string{"maro@krisam.in"},
		"subject": "normal mail", "textBody": "hey",
	})
	if code != 200 {
		t.Fatalf("send: %d", code)
	}
	code, page, _ = callAs(t, srv, "maro@krisam.in", "", "GET", "/api/me/message?mailbox=INBOX", nil)
	rowList := page["messageList"].([]any)
	if code != 200 || len(rowList) != 1 || rowList[0].(map[string]any)["seen"] != true {
		t.Fatalf("markSeen applied: %v", page)
	}
	t.Log("✔ delivery path — markSeen delivers read")

	// 8) update — deactivate the discard rule, mail passes again
	code, _, _ = callAs(t, srv, "maro@krisam.in", "", "PUT",
		fmt.Sprintf("/api/me/filter/%s", rule2ID), map[string]any{
			"name": "drop", "active": false, "field": "subject", "matchType": "contains",
			"pattern": "buy now", "action": "discard",
		})
	if code != 204 {
		t.Fatalf("update: %d", code)
	}
	code, _, _ = callAs(t, srv, "guest@krisam.in", "", "POST", "/api/me/send", map[string]any{
		"from": "guest@krisam.in", "toList": []string{"maro@krisam.in"},
		"subject": "buy now again", "textBody": "x",
	})
	if code != 200 {
		t.Fatalf("send: %d", code)
	}
	code, page, _ = callAs(t, srv, "maro@krisam.in", "", "GET", "/api/me/message?mailbox=INBOX", nil)
	if code != 200 || len(page["messageList"].([]any)) != 2 {
		t.Fatalf("deactivated rule must not discard: %v", page)
	}
	t.Log("✔ deactivate — inactive rules skipped")
}
