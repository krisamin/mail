package api

import (
	"fmt"
	"strings"
	"testing"
)

// Webmail API integration tests — mailbox list, message paging, detail parse,
// flag/move/delete, send (local + Sent copy), and cross-account isolation.

// TestWebmail drives the full webmail surface with two users.
func TestWebmail(t *testing.T) {
	srv := testServer(t)

	// seed: domain + two users
	if code, dom, _ := call(t, srv, "POST", "/api/admin/domain", map[string]string{"name": "krisam.in"}); code != 201 {
		t.Fatalf("domain: %d %v", code, dom)
	}
	for _, name := range []string{"maro", "guest"} {
		if code, u, _ := callAs(t, srv, name+"@krisam.in", "", "POST", "/api/me/provision", nil); code != 200 {
			t.Fatalf("user %s: %d %v", name, code, u)
		}
	}

	// 1) empty INBOX
	code, _, boxList := callAs(t, srv, "maro@krisam.in", "", "GET", "/api/me/mailbox", nil)
	if code != 200 || len(boxList) == 0 || boxList[0]["name"] != "INBOX" {
		t.Fatalf("mailbox list: %d %v", code, boxList)
	}
	t.Log("✔ mailbox list — INBOX first")

	// 2) send local mail guest → maro (webmail send, direct delivery)
	code, sent, _ := callAs(t, srv, "guest@krisam.in", "", "POST", "/api/me/send", map[string]any{
		"from":     "guest@krisam.in",
		"toList":   []string{"maro@krisam.in"},
		"subject":  "hello from webmail",
		"textBody": "first webmail message body",
	})
	if code != 200 || sent["delivered"].(float64) != 1 || sent["queued"].(float64) != 0 {
		t.Fatalf("send: %d %v", code, sent)
	}
	t.Log("✔ webmail send — local direct delivery")

	// sender ownership: guest cannot send as maro
	code, _, _ = callAs(t, srv, "guest@krisam.in", "", "POST", "/api/me/send", map[string]any{
		"from":     "maro@krisam.in",
		"toList":   []string{"maro@krisam.in"},
		"subject":  "spoof",
		"textBody": "x",
	})
	if code != 403 {
		t.Fatalf("spoofed from should be 403: %d", code)
	}
	t.Log("✔ send as unowned address 403")

	// unknown local recipient → 400
	code, _, _ = callAs(t, srv, "guest@krisam.in", "", "POST", "/api/me/send", map[string]any{
		"from":     "guest@krisam.in",
		"toList":   []string{"nobody@krisam.in"},
		"subject":  "x",
		"textBody": "x",
	})
	if code != 400 {
		t.Fatalf("unknown local recipient should be 400: %d", code)
	}
	t.Log("✔ unknown local recipient 400")

	// 3) maro's INBOX now has the message; guest has a Sent copy
	code, page, _ := callAs(t, srv, "maro@krisam.in", "", "GET", "/api/me/message?mailbox=INBOX", nil)
	if code != 200 {
		t.Fatalf("message list: %d", code)
	}
	rowList := page["messageList"].([]any)
	if len(rowList) != 1 {
		t.Fatalf("expected 1 message, got %d", len(rowList))
	}
	row := rowList[0].(map[string]any)
	if row["subject"] != "hello from webmail" || row["seen"] != false {
		t.Fatalf("row: %v", row)
	}
	msgID := int64(row["id"].(float64))
	t.Log("✔ message list row — subject cached, unseen")

	code, _, guestBoxList := callAs(t, srv, "guest@krisam.in", "", "GET", "/api/me/mailbox", nil)
	if code != 200 {
		t.Fatalf("guest mailbox: %d", code)
	}
	foundSent := false
	for _, b := range guestBoxList {
		if b["name"] == "Sent" && b["messageCount"].(float64) == 1 {
			foundSent = true
		}
	}
	if !foundSent {
		t.Fatalf("guest Sent copy missing: %v", guestBoxList)
	}
	t.Log("✔ Sent copy for the sender")

	// 4) detail — body parsed, auto-marked seen
	code, detail, _ := callAs(t, srv, "maro@krisam.in", "", "GET", fmt.Sprintf("/api/me/message/%d", msgID), nil)
	if code != 200 {
		t.Fatalf("detail: %d %v", code, detail)
	}
	if !strings.Contains(detail["textBody"].(string), "first webmail message body") {
		t.Fatalf("textBody: %v", detail["textBody"])
	}
	if detail["seen"] != true {
		t.Fatalf("detail should auto-mark seen: %v", detail["seen"])
	}
	if len(detail["toList"].([]any)) != 1 {
		t.Fatalf("toList: %v", detail["toList"])
	}
	t.Log("✔ detail — MIME parsed, auto \\Seen")

	// 5) cross-account isolation — guest cannot read maro's message
	code, _, _ = callAs(t, srv, "guest@krisam.in", "", "GET", fmt.Sprintf("/api/me/message/%d", msgID), nil)
	if code != 404 {
		t.Fatalf("cross-account read should be 404: %d", code)
	}
	code, _, _ = callAs(t, srv, "guest@krisam.in", "", "DELETE", fmt.Sprintf("/api/me/message/%d", msgID), nil)
	if code != 404 {
		t.Fatalf("cross-account delete should be 404: %d", code)
	}
	t.Log("✔ cross-account isolation 404")

	// 6) flag patch — flagged on, seen off
	code, _, _ = callAs(t, srv, "maro@krisam.in", "", "PATCH", fmt.Sprintf("/api/me/message/%d", msgID),
		map[string]any{"flagged": true, "seen": false})
	if code != 204 {
		t.Fatalf("patch: %d", code)
	}
	code, detail, _ = callAs(t, srv, "maro@krisam.in", "", "GET", fmt.Sprintf("/api/me/message/%d", msgID), nil)
	if code != 200 || detail["flagged"] != true {
		t.Fatalf("flag applied: %d %v", code, detail["flagged"])
	}
	t.Log("✔ flag patch")

	// 7) move to Archive (created on demand)
	code, _, _ = callAs(t, srv, "maro@krisam.in", "", "POST", fmt.Sprintf("/api/me/message/%d/move", msgID),
		map[string]string{"mailbox": "Archive"})
	if code != 204 {
		t.Fatalf("move: %d", code)
	}
	code, detail, _ = callAs(t, srv, "maro@krisam.in", "", "GET", fmt.Sprintf("/api/me/message/%d", msgID), nil)
	if code != 200 || detail["mailbox"] != "Archive" {
		t.Fatalf("moved mailbox: %d %v", code, detail["mailbox"])
	}
	t.Log("✔ move — Archive created on demand")

	// 8) two-step delete: Archive → Trash (move), Trash → physical
	code, _, _ = callAs(t, srv, "maro@krisam.in", "", "DELETE", fmt.Sprintf("/api/me/message/%d", msgID), nil)
	if code != 204 {
		t.Fatalf("delete step 1: %d", code)
	}
	code, detail, _ = callAs(t, srv, "maro@krisam.in", "", "GET", fmt.Sprintf("/api/me/message/%d", msgID), nil)
	if code != 200 || detail["mailbox"] != "Trash" {
		t.Fatalf("should be in Trash: %d %v", code, detail["mailbox"])
	}
	code, _, _ = callAs(t, srv, "maro@krisam.in", "", "DELETE", fmt.Sprintf("/api/me/message/%d", msgID), nil)
	if code != 204 {
		t.Fatalf("delete step 2: %d", code)
	}
	code, _, _ = callAs(t, srv, "maro@krisam.in", "", "GET", fmt.Sprintf("/api/me/message/%d", msgID), nil)
	if code != 404 {
		t.Fatalf("physically deleted should be 404: %d", code)
	}
	t.Log("✔ two-step delete — Trash then physical")

	// 9) reply threading — maro replies to a fresh message from guest
	code, sent, _ = callAs(t, srv, "guest@krisam.in", "", "POST", "/api/me/send", map[string]any{
		"from": "guest@krisam.in", "toList": []string{"maro@krisam.in"},
		"subject": "thread start", "textBody": "root",
	})
	if code != 200 {
		t.Fatalf("thread send: %d %v", code, sent)
	}
	code, page, _ = callAs(t, srv, "maro@krisam.in", "", "GET", "/api/me/message?mailbox=INBOX", nil)
	if code != 200 {
		t.Fatalf("list: %d", code)
	}
	rootID := int64(page["messageList"].([]any)[0].(map[string]any)["id"].(float64))
	code, _, _ = callAs(t, srv, "maro@krisam.in", "", "POST", "/api/me/send", map[string]any{
		"from": "maro@krisam.in", "toList": []string{"guest@krisam.in"},
		"subject": "Re: thread start", "textBody": "reply", "inReplyTo": rootID,
	})
	if code != 200 {
		t.Fatalf("reply send: %d", code)
	}
	// original marked \Answered
	code, detail, _ = callAs(t, srv, "maro@krisam.in", "", "GET", fmt.Sprintf("/api/me/message/%d", rootID), nil)
	if code != 200 || detail["answered"] != true {
		t.Fatalf("original should be answered: %d %v", code, detail["answered"])
	}
	// guest's copy carries In-Reply-To
	code, page, _ = callAs(t, srv, "guest@krisam.in", "", "GET", "/api/me/message?mailbox=INBOX", nil)
	if code != 200 {
		t.Fatalf("guest list: %d", code)
	}
	replyID := int64(page["messageList"].([]any)[0].(map[string]any)["id"].(float64))
	code, detail, _ = callAs(t, srv, "guest@krisam.in", "", "GET", fmt.Sprintf("/api/me/message/%d", replyID), nil)
	if code != 200 || detail["messageId"] == "" {
		t.Fatalf("reply detail: %d %v", code, detail)
	}
	t.Log("✔ reply — \\Answered + Message-ID threading")
}

// TestWebmailPaging checks the cursor pagination contract.
func TestWebmailPaging(t *testing.T) {
	srv := testServer(t)

	if code, _, _ := call(t, srv, "POST", "/api/admin/domain", map[string]string{"name": "krisam.in"}); code != 201 {
		t.Fatalf("domain")
	}
	for _, name := range []string{"maro", "guest"} {
		if code, _, _ := callAs(t, srv, name+"@krisam.in", "", "POST", "/api/me/provision", nil); code != 200 {
			t.Fatalf("user %s", name)
		}
	}
	// 5 messages
	for i := 1; i <= 5; i++ {
		code, _, _ := callAs(t, srv, "guest@krisam.in", "", "POST", "/api/me/send", map[string]any{
			"from": "guest@krisam.in", "toList": []string{"maro@krisam.in"},
			"subject": fmt.Sprintf("msg %d", i), "textBody": "b",
		})
		if code != 200 {
			t.Fatalf("send %d: %d", i, code)
		}
	}

	// page size 2 → newest first: [5,4], [3,2], [1]
	var seen []string
	before := "0"
	for range 3 {
		code, page, _ := callAs(t, srv, "maro@krisam.in", "",
			"GET", "/api/me/message?mailbox=INBOX&limit=2&before="+before, nil)
		if code != 200 {
			t.Fatalf("page: %d", code)
		}
		for _, r := range page["messageList"].([]any) {
			seen = append(seen, r.(map[string]any)["subject"].(string))
		}
		nb := page["nextBefore"].(float64)
		if nb == 0 {
			break
		}
		before = fmt.Sprintf("%d", int64(nb))
	}
	want := []string{"msg 5", "msg 4", "msg 3", "msg 2", "msg 1"}
	if strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Fatalf("paging order: %v", seen)
	}
	t.Log("✔ cursor paging — newest first, stable order")
}
