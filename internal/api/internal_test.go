package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/krisamin/mail/internal/store/postgres"
	"github.com/krisamin/mail/internal/webhook"
)

// Internal API (/internal/v1) integration tests — service token, account
// header, provisioning, sync cursor, flags, cross-account isolation, raw
// send, and the mail event webhook end to end. Requires dev Postgres
// (MAIL_TEST_DSN), like the rest of this package.

const testServiceToken = "svc-token-for-test"

// internalServer starts the API with the internal routes (token "" = off)
// and returns the server plus the store for direct seeding.
func internalServer(t *testing.T, token string) (*httptest.Server, *postgres.Store) {
	t.Helper()
	dsn := os.Getenv("MAIL_TEST_DSN")
	if dsn == "" {
		t.Skip("MAIL_TEST_DSN not set — skipping integration tests")
	}
	st, err := postgres.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(st.Close)
	_, _ = st.Pool().Exec(context.Background(),
		`TRUNCATE domain, account, send_counter, app_password, mailbox, message, message_flag, message_blob, outbound_queue, address, relay, setting, filter_rule RESTART IDENTITY CASCADE`)
	auth, err := NewAuthenticator(context.Background(), AuthConfig{
		AdminGroup: "mail-admin", InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	srv := httptest.NewServer(NewServer(st, auth).WithHostname("mail.example.test").WithInternal(token))
	t.Cleanup(srv.Close)
	return srv, st
}

// icall makes one internal request. token/account "" = header omitted.
func icall(t *testing.T, srv *httptest.Server, method, path, token, account string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, srv.URL+path, rd)
	if err != nil {
		t.Fatalf("req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if account != "" {
		req.Header.Set("X-Mail-Account", account)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// ijson is icall + JSON object decode.
func ijson(t *testing.T, srv *httptest.Server, method, path, account string, body any) (int, map[string]any) {
	t.Helper()
	code, raw := icall(t, srv, method, path, testServiceToken, account, body)
	var obj map[string]any
	_ = json.Unmarshal(raw, &obj)
	return code, obj
}

// provisionAs creates (or finds) an account through the internal API.
func provisionAs(t *testing.T, srv *httptest.Server, subject, email string) (string, []any) {
	t.Helper()
	code, out := ijson(t, srv, "POST", "/internal/v1/account", "", map[string]string{
		"subject": subject, "email": email, "name": "Test",
	})
	if code != 200 {
		t.Fatalf("provision %s: %d %v", email, code, out)
	}
	return out["accountId"].(string), out["addressList"].([]any)
}

// appendTo puts a raw message into an account mailbox directly.
func appendTo(t *testing.T, st *postgres.Store, accountID, mailbox, raw string) {
	t.Helper()
	box, err := st.EnsureMailbox(context.Background(), uuid.MustParse(accountID), mailbox)
	if err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	if _, err := st.AppendMessage(context.Background(), box.ID, []byte(raw), nil, time.Now()); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func testMail(n int, extra string) string {
	return "From: \"Alice Kim\" <alice@example.org>\r\n" +
		"To: maro@ara.test, \"Bob\" <bob@example.org>\r\n" +
		"Cc: carol@example.org\r\n" +
		"Subject: sync test " + string(rune('0'+n)) + "\r\n" +
		"Date: Tue, 03 Nov 2026 10:00:0" + string(rune('0'+n)) + " +0900\r\n" +
		"Message-ID: <m" + string(rune('0'+n)) + "@example.org>\r\n" +
		extra +
		"Content-Type: text/plain; charset=utf-8\r\n\r\nbody number " + string(rune('0'+n)) + "\r\n"
}

// TestInternalAuth — routes absent without a token, 401 on a missing/wrong
// token, 400/404 on a missing/malformed/unknown account header.
func TestInternalAuth(t *testing.T) {
	off, _ := internalServer(t, "")
	if code, _ := icall(t, off, "POST", "/internal/v1/account", testServiceToken, "", map[string]string{"subject": "ara:x", "email": "x@ara.test"}); code != 404 {
		t.Fatalf("token env empty should be 404, got %d", code)
	}
	if code, _ := icall(t, off, "GET", "/internal/v1/mailbox", testServiceToken, uuid.NewString(), nil); code != 404 {
		t.Fatalf("token env empty should be 404 (scoped), got %d", code)
	}
	t.Log("✔ no MAIL_INTERNAL_TOKEN → routes absent (404)")

	srv, _ := internalServer(t, testServiceToken)
	body := map[string]string{"subject": "ara:m1", "email": "maro@ara.test"}
	if code, _ := icall(t, srv, "POST", "/internal/v1/account", "", "", body); code != 401 {
		t.Fatalf("missing token should be 401, got %d", code)
	}
	if code, _ := icall(t, srv, "POST", "/internal/v1/account", "wrong-token", "", body); code != 401 {
		t.Fatalf("wrong token should be 401, got %d", code)
	}
	if code, _ := icall(t, srv, "POST", "/internal/v1/account", testServiceToken+"x", "", body); code != 401 {
		t.Fatalf("token with suffix should be 401, got %d", code)
	}
	if code, _ := icall(t, srv, "GET", "/internal/v1/mailbox", "wrong-token", uuid.NewString(), nil); code != 401 {
		t.Fatalf("wrong token on scoped route should be 401, got %d", code)
	}
	if code, _ := icall(t, srv, "POST", "/internal/v1/account", testServiceToken, "", body); code != 200 {
		t.Fatalf("right token should be 200, got %d", code)
	}
	t.Log("✔ token missing/wrong → 401, right → 200")

	if code, _ := icall(t, srv, "GET", "/internal/v1/mailbox", testServiceToken, "", nil); code != 400 {
		t.Fatalf("missing account header should be 400, got %d", code)
	}
	if code, _ := icall(t, srv, "GET", "/internal/v1/mailbox", testServiceToken, "not-a-uuid", nil); code != 400 {
		t.Fatalf("malformed account header should be 400, got %d", code)
	}
	if code, _ := icall(t, srv, "GET", "/internal/v1/mailbox", testServiceToken, uuid.NewString(), nil); code != 404 {
		t.Fatalf("unknown account should be 404, got %d", code)
	}
	t.Log("✔ X-Mail-Account missing/malformed → 400, unknown → 404")
}

// TestInternalProvision — idempotent account+address+INBOX, bare account
// for an unregistered domain, address add with 409 on conflict.
func TestInternalProvision(t *testing.T) {
	srv, st := internalServer(t, testServiceToken)
	if _, err := st.CreateDomain(context.Background(), "ara.test"); err != nil {
		t.Fatalf("domain: %v", err)
	}

	id1, list1 := provisionAs(t, srv, "ara:m1", "Maro@ara.test")
	if len(list1) != 1 || list1[0].(map[string]any)["address"] != "maro@ara.test" {
		t.Fatalf("addressList: %v", list1)
	}
	id2, list2 := provisionAs(t, srv, "ara:m1", "maro@ara.test")
	if id1 != id2 || len(list2) != 1 {
		t.Fatalf("provision not idempotent: %s vs %s, %v", id1, id2, list2)
	}
	var subject, kind string
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT oidc_subject, kind FROM account WHERE id = $1`, id1).Scan(&subject, &kind); err != nil {
		t.Fatalf("account row: %v", err)
	}
	if subject != "ara:m1" || kind != "user" {
		t.Fatalf("account row: subject=%s kind=%s", subject, kind)
	}
	code, boxList := icall(t, srv, "GET", "/internal/v1/mailbox", testServiceToken, id1, nil)
	if code != 200 || !strings.Contains(string(boxList), `"name":"INBOX"`) {
		t.Fatalf("mailbox after provision: %d %s", code, boxList)
	}
	t.Log("✔ provision idempotent (subject kept, kind user, INBOX)")

	_, bare := provisionAs(t, srv, "ara:m2", "someone@unregistered.test")
	if len(bare) != 0 {
		t.Fatalf("unregistered domain should give a bare account: %v", bare)
	}
	t.Log("✔ unregistered domain → bare account, addressList []")

	code, addr := ijson(t, srv, "POST", "/internal/v1/account/"+id1+"/address", "",
		map[string]string{"localPart": "Hello", "domain": "ara.test"})
	if code != 201 || addr["address"] != "hello@ara.test" || addr["id"] == "" {
		t.Fatalf("add address: %d %v", code, addr)
	}
	if code, _ := ijson(t, srv, "POST", "/internal/v1/account/"+id1+"/address", "",
		map[string]string{"localPart": "hello", "domain": "ara.test"}); code != 409 {
		t.Fatalf("duplicate address should be 409, got %d", code)
	}
	if code, _ := ijson(t, srv, "POST", "/internal/v1/account/"+id1+"/address", "",
		map[string]string{"localPart": "x", "domain": "nope.test"}); code != 400 {
		t.Fatalf("unknown domain should be 400, got %d", code)
	}
	if code, _ := ijson(t, srv, "POST", "/internal/v1/account/"+id1+"/address", "",
		map[string]string{"localPart": "*", "domain": "ara.test"}); code != 400 {
		t.Fatalf("catch-all should be 400, got %d", code)
	}
	if code, _ := ijson(t, srv, "POST", "/internal/v1/account/"+uuid.NewString()+"/address", "",
		map[string]string{"localPart": "y", "domain": "ara.test"}); code != 404 {
		t.Fatalf("unknown account should be 404, got %d", code)
	}
	_, list3 := provisionAs(t, srv, "ara:m1", "maro@ara.test")
	if len(list3) != 2 {
		t.Fatalf("provision should now list 2 addresses: %v", list3)
	}
	t.Log("✔ address add 201, duplicate 409, unknown domain 400, '*' 400")
}

// TestInternalSync — cursor order, paging, parsed headers, cache, flags,
// no implicit \Seen, raw bytes, expunge visible in the flag listing.
func TestInternalSync(t *testing.T) {
	srv, st := internalServer(t, testServiceToken)
	if _, err := st.CreateDomain(context.Background(), "ara.test"); err != nil {
		t.Fatalf("domain: %v", err)
	}
	acct, _ := provisionAs(t, srv, "ara:m1", "maro@ara.test")

	mail1 := testMail(1, "")
	mail2 := testMail(2, "In-Reply-To: <m1@example.org>\r\nReferences: <m0@example.org> <m1@example.org>\r\n")
	mail3 := "From: alice@example.org\r\nTo: maro@ara.test\r\nSubject: with file\r\n" +
		"Message-ID: <m3@example.org>\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=XX\r\n\r\n" +
		"--XX\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n" +
		"--XX\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=a.pdf\r\n\r\nPDFDATA\r\n" +
		"--XX--\r\n"
	for _, raw := range []string{mail1, mail2, mail3} {
		appendTo(t, st, acct, "INBOX", raw)
	}

	code, boxRaw := icall(t, srv, "GET", "/internal/v1/mailbox", testServiceToken, acct, nil)
	var boxList []map[string]any
	_ = json.Unmarshal(boxRaw, &boxList)
	if code != 200 || len(boxList) != 1 || boxList[0]["uidNext"].(float64) != 4 ||
		boxList[0]["uidValidity"].(float64) <= 0 || boxList[0]["messageCount"].(float64) != 3 {
		t.Fatalf("mailbox: %d %s", code, boxRaw)
	}
	t.Log("✔ mailbox carries uidValidity/uidNext")

	code, page := ijson(t, srv, "GET", "/internal/v1/sync?mailbox=INBOX&sinceUid=0&limit=2", acct, nil)
	if code != 200 || page["hasMore"] != true || page["uidNext"].(float64) != 4 {
		t.Fatalf("sync page 1: %d %v", code, page)
	}
	list := page["messageList"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["uid"].(float64) != 1 || list[1].(map[string]any)["uid"].(float64) != 2 {
		t.Fatalf("sync page 1 order: %v", list)
	}
	m1 := list[0].(map[string]any)
	from := m1["from"].(map[string]any)
	if m1["messageId"] != "m1@example.org" || from["name"] != "Alice Kim" || from["address"] != "alice@example.org" ||
		len(m1["toList"].([]any)) != 2 || len(m1["ccList"].([]any)) != 1 || m1["subject"] != "sync test 1" ||
		m1["date"] != "2026-11-03T01:00:01Z" || m1["preview"] != "body number 1" || m1["hasAttachment"] != false ||
		len(m1["flagList"].([]any)) != 0 || len(m1["referenceList"].([]any)) != 0 {
		t.Fatalf("message 1 fields: %v", m1)
	}
	m2 := list[1].(map[string]any)
	refList := m2["referenceList"].([]any)
	if m2["inReplyTo"] != "m1@example.org" || len(refList) != 2 || refList[1] != "m1@example.org" {
		t.Fatalf("message 2 threading: %v", m2)
	}
	t.Log("✔ sync page 1: uid ascending, hasMore, parsed headers + threading")

	code, page = ijson(t, srv, "GET", "/internal/v1/sync?mailbox=INBOX&sinceUid=2", acct, nil)
	list = page["messageList"].([]any)
	if code != 200 || page["hasMore"] != false || len(list) != 1 ||
		list[0].(map[string]any)["uid"].(float64) != 3 || list[0].(map[string]any)["hasAttachment"] != true {
		t.Fatalf("sync page 2: %d %v", code, page)
	}
	var cached int
	_ = st.Pool().QueryRow(context.Background(), `SELECT count(*) FROM message WHERE sync_meta IS NOT NULL`).Scan(&cached)
	if cached != 3 {
		t.Fatalf("sync_meta should be cached for 3 rows, got %d", cached)
	}
	// the cached path answers the same
	code, page = ijson(t, srv, "GET", "/internal/v1/sync?sinceUid=1&limit=1", acct, nil)
	list = page["messageList"].([]any)
	if code != 200 || len(list) != 1 || list[0].(map[string]any)["inReplyTo"] != "m1@example.org" {
		t.Fatalf("cached sync: %d %v", code, page)
	}
	if code, _ := ijson(t, srv, "GET", "/internal/v1/sync?sinceUid=abc", acct, nil); code != 400 {
		t.Fatalf("bad sinceUid should be 400, got %d", code)
	}
	if code, _ := ijson(t, srv, "GET", "/internal/v1/sync?mailbox=Nope", acct, nil); code != 404 {
		t.Fatalf("unknown mailbox should be 404, got %d", code)
	}
	t.Log("✔ sync page 2 + cache (sync_meta 3 rows), bad cursor 400, unknown mailbox 404")

	id1 := m1["id"].(string)
	id2 := m2["id"].(string)
	code, res := ijson(t, srv, "POST", "/internal/v1/message/batch", acct,
		map[string]any{"idList": []string{id1}, "action": "seen"})
	if code != 200 || res["done"].(float64) != 1 {
		t.Fatalf("batch seen: %d %v", code, res)
	}
	// detail must not mark message 2 seen
	code, detail := ijson(t, srv, "GET", "/internal/v1/message/"+id2, acct, nil)
	if code != 200 || detail["textBody"] != "body number 2\r\n" || detail["seen"] != false {
		t.Fatalf("detail: %d %v", code, detail)
	}
	code, flags := ijson(t, srv, "GET", "/internal/v1/flags?mailbox=INBOX&fromUid=1", acct, nil)
	rowList := flags["flagList"].([]any)
	if code != 200 || len(rowList) != 3 || flags["truncated"] != false {
		t.Fatalf("flags: %d %v", code, flags)
	}
	r1 := rowList[0].(map[string]any)
	r2 := rowList[1].(map[string]any)
	if r1["uid"].(float64) != 1 || len(r1["flagList"].([]any)) != 1 || r1["flagList"].([]any)[0] != "\\Seen" ||
		r2["uid"].(float64) != 2 || len(r2["flagList"].([]any)) != 0 {
		t.Fatalf("flag rows: %v", rowList)
	}
	code, flags = ijson(t, srv, "GET", "/internal/v1/flags?fromUid=3", acct, nil)
	if code != 200 || len(flags["flagList"].([]any)) != 1 {
		t.Fatalf("flags fromUid=3: %d %v", code, flags)
	}
	t.Log("✔ batch seen, detail without implicit \\Seen, flags from a uid")

	code, raw := icall(t, srv, "GET", "/internal/v1/message/"+id2+"/raw", testServiceToken, acct, nil)
	if code != 200 || string(raw) != mail2 {
		t.Fatalf("raw: %d %q", code, raw)
	}
	t.Log("✔ raw returns the original bytes")

	// delete → moves to Trash: uid 2 disappears from the INBOX listing
	if code, res := ijson(t, srv, "POST", "/internal/v1/message/batch", acct,
		map[string]any{"idList": []string{id2}, "action": "delete"}); code != 200 || res["done"].(float64) != 1 {
		t.Fatalf("batch delete: %d %v", code, res)
	}
	_, flags = ijson(t, srv, "GET", "/internal/v1/flags", acct, nil)
	for _, row := range flags["flagList"].([]any) {
		if row.(map[string]any)["uid"].(float64) == 2 {
			t.Fatalf("uid 2 should be gone from INBOX: %v", flags)
		}
	}
	t.Log("✔ expunged uid absent from the flag listing")
}

// TestInternalCrossAccount — another account's message id is a 404 on
// every message route, and batch cannot touch it.
func TestInternalCrossAccount(t *testing.T) {
	srv, st := internalServer(t, testServiceToken)
	if _, err := st.CreateDomain(context.Background(), "ara.test"); err != nil {
		t.Fatalf("domain: %v", err)
	}
	acctA, _ := provisionAs(t, srv, "ara:a", "alpha@ara.test")
	acctB, _ := provisionAs(t, srv, "ara:b", "beta@ara.test")
	appendTo(t, st, acctB, "INBOX", testMail(1, ""))

	_, page := ijson(t, srv, "GET", "/internal/v1/sync", acctB, nil)
	idB := page["messageList"].([]any)[0].(map[string]any)["id"].(string)

	for _, path := range []string{"/internal/v1/message/" + idB, "/internal/v1/message/" + idB + "/raw",
		"/internal/v1/message/" + idB + "/attachment/0"} {
		if code, _ := icall(t, srv, "GET", path, testServiceToken, acctA, nil); code != 404 {
			t.Fatalf("%s as A should be 404, got %d", path, code)
		}
	}
	code, res := ijson(t, srv, "POST", "/internal/v1/message/batch", acctA,
		map[string]any{"idList": []string{idB}, "action": "seen"})
	if code != 200 || res["done"].(float64) != 0 || res["failed"].(float64) != 1 {
		t.Fatalf("batch on foreign id: %d %v", code, res)
	}
	_, flags := ijson(t, srv, "GET", "/internal/v1/flags", acctB, nil)
	if len(flags["flagList"].([]any)[0].(map[string]any)["flagList"].([]any)) != 0 {
		t.Fatalf("B's message must stay unseen: %v", flags)
	}
	_, page = ijson(t, srv, "GET", "/internal/v1/sync", acctA, nil)
	if len(page["messageList"].([]any)) != 0 {
		t.Fatalf("A's sync must not see B's mail: %v", page)
	}
	t.Log("✔ foreign message id → 404 / batch failed, B untouched, A's sync empty")
}

// TestInternalSend — raw send: header From ownership, envelopeFrom, Bcc
// refusal, external-send policy, local delivery byte-exact (iMIP), Sent copy,
// Message-ID/Date added only when missing.
func TestInternalSend(t *testing.T) {
	srv, st := internalServer(t, testServiceToken)
	if _, err := st.CreateDomain(context.Background(), "ara.test"); err != nil {
		t.Fatalf("domain: %v", err)
	}
	acctA, _ := provisionAs(t, srv, "ara:a", "maro@ara.test")
	acctB, _ := provisionAs(t, srv, "ara:b", "guest@ara.test")

	send := func(account, raw string, extra map[string]any) (int, map[string]any) {
		body := map[string]any{"raw": base64.StdEncoding.EncodeToString([]byte(raw))}
		for k, v := range extra {
			body[k] = v
		}
		return ijson(t, srv, "POST", "/internal/v1/send", account, body)
	}

	invite := "From: Maro <maro@ara.test>\r\nTo: guest@ara.test\r\nSubject: Invitation: sync\r\n" +
		"Date: Wed, 04 Nov 2026 18:00:00 +0900\r\nMessage-ID: <inv-1@ara.test>\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"b1\"\r\n\r\n" +
		"--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nYou are invited.\r\n" +
		"--b1\r\nContent-Type: text/calendar; charset=utf-8; method=REQUEST\r\n\r\n" +
		"BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:evt-1@ara.test\r\n" +
		"DTSTART:20261105T010000Z\r\nSUMMARY:sync\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n" +
		"--b1--\r\n"

	// header From not owned by the calling account
	if code, out := send(acctB, invite, nil); code != 403 {
		t.Fatalf("foreign header From should be 403: %d %v", code, out)
	}
	// envelopeFrom not owned
	if code, out := send(acctA, invite, map[string]any{"envelopeFrom": "guest@ara.test"}); code != 403 {
		t.Fatalf("foreign envelopeFrom should be 403: %d %v", code, out)
	}
	// Bcc in raw
	if code, out := send(acctA, "Bcc: x@ara.test\r\n"+invite, nil); code != 400 {
		t.Fatalf("Bcc header should be 400: %d %v", code, out)
	}
	// external recipient — the default group denies external send
	if code, out := send(acctA, invite, map[string]any{"recipientList": []string{"someone@example.org"}}); code != 403 {
		t.Fatalf("external send should be refused by policy: %d %v", code, out)
	}
	var queued int
	_ = st.Pool().QueryRow(context.Background(), `SELECT count(*) FROM outbound_queue`).Scan(&queued)
	if queued != 0 {
		t.Fatalf("nothing may be queued after refusals, got %d", queued)
	}
	t.Log("✔ header From / envelopeFrom ownership 403, Bcc 400, external policy 403")

	code, out := send(acctA, invite, nil)
	if code != 200 || out["messageId"] != "inv-1@ara.test" || out["delivered"].(float64) != 1 || out["queued"].(float64) != 0 {
		t.Fatalf("send: %d %v", code, out)
	}
	_, page := ijson(t, srv, "GET", "/internal/v1/sync", acctB, nil)
	list := page["messageList"].([]any)
	if len(list) != 1 {
		t.Fatalf("guest INBOX: %v", page)
	}
	idB := list[0].(map[string]any)["id"].(string)
	_, raw := icall(t, srv, "GET", "/internal/v1/message/"+idB+"/raw", testServiceToken, acctB, nil)
	if string(raw) != invite {
		t.Fatalf("delivered bytes differ from the sent raw:\n%q", raw)
	}
	_, sentPage := ijson(t, srv, "GET", "/internal/v1/sync?mailbox=Sent", acctA, nil)
	if sl := sentPage["messageList"].([]any); len(sl) != 1 || len(sl[0].(map[string]any)["flagList"].([]any)) != 1 {
		t.Fatalf("Sent copy: %v", sentPage)
	}
	t.Log("✔ iMIP raw delivered byte-exact (text/calendar intact) + Sent copy \\Seen")

	// no Message-ID / Date → both prepended, body untouched
	bare := "From: maro@ara.test\r\nTo: guest@ara.test\r\nSubject: bare\r\n\r\nhello\r\n"
	code, out = send(acctA, bare, nil)
	if code != 200 || !strings.HasSuffix(out["messageId"].(string), "@mail.example.test") {
		t.Fatalf("bare send: %d %v", code, out)
	}
	_, page = ijson(t, srv, "GET", "/internal/v1/sync?sinceUid=1", acctB, nil)
	idBare := page["messageList"].([]any)[0].(map[string]any)["id"].(string)
	_, raw = icall(t, srv, "GET", "/internal/v1/message/"+idBare+"/raw", testServiceToken, acctB, nil)
	if !strings.HasPrefix(string(raw), "Message-ID: <"+out["messageId"].(string)+">\r\nDate: ") || !strings.HasSuffix(string(raw), bare) {
		t.Fatalf("bare raw: %q", raw)
	}
	t.Log("✔ missing Message-ID/Date prepended, original bytes kept as suffix")
}

// TestInternalWebhook — store changes → pg_notify mail_event → Notifier →
// emitter → signed POST, for new mail, a flag change and a move (expunge +
// new). Retries/signature edge cases are unit-tested in internal/webhook.
func TestInternalWebhook(t *testing.T) {
	srv, st := internalServer(t, testServiceToken)
	if _, err := st.CreateDomain(context.Background(), "ara.test"); err != nil {
		t.Fatalf("domain: %v", err)
	}
	acct, _ := provisionAs(t, srv, "ara:m1", "maro@ara.test")

	const secret = "hook-secret"
	var mu sync.Mutex
	var got []webhook.Event
	badSig := 0
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		if !webhook.Verify(secret, body, r.Header.Get(webhook.HeaderSignature)) {
			badSig++
			w.WriteHeader(401)
			return
		}
		var ev webhook.Event
		_ = json.Unmarshal(body, &ev)
		if ev.EventID == "" || ev.EventID != r.Header.Get(webhook.HeaderEventID) {
			badSig++
		}
		got = append(got, ev)
	}))
	t.Cleanup(receiver.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	notifier := postgres.NewNotifier(st)
	emitter := webhook.New(webhook.Config{URL: receiver.URL, Secret: secret})
	evCh, _ := notifier.SubscribeEvent(64)
	go emitter.Pump(ctx, evCh)
	go emitter.Run(ctx)
	go notifier.Run(ctx)

	waitFor := func(what string, match func(webhook.Event) bool) webhook.Event {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			for _, ev := range got {
				if match(ev) {
					mu.Unlock()
					return ev
				}
			}
			mu.Unlock()
			time.Sleep(50 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("no %s event; got %+v (bad signatures %d)", what, got, badSig)
		return webhook.Event{}
	}

	// LISTEN starts asynchronously — poke a scratch mailbox until it shows up
	warm := time.Now().Add(5 * time.Second)
	for {
		appendTo(t, st, acct, "Warmup", testMail(9, ""))
		time.Sleep(100 * time.Millisecond)
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 || time.Now().After(warm) {
			break
		}
	}

	appendTo(t, st, acct, "INBOX", testMail(1, ""))
	ev := waitFor("mail.new INBOX", func(e webhook.Event) bool { return e.Kind == "mail.new" && e.Mailbox == "INBOX" })
	if ev.AccountID != acct || ev.UID == nil || *ev.UID != 1 || ev.UIDValidity == 0 || ev.OccurredAt == "" {
		t.Fatalf("mail.new event: %+v", ev)
	}
	t.Log("✔ delivery → mail.new (account, mailbox, uidValidity, uid, signed)")

	_, page := ijson(t, srv, "GET", "/internal/v1/sync", acct, nil)
	id := page["messageList"].([]any)[0].(map[string]any)["id"].(string)
	ijson(t, srv, "POST", "/internal/v1/message/batch", acct, map[string]any{"idList": []string{id}, "action": "flag"})
	ev = waitFor("mail.flags", func(e webhook.Event) bool { return e.Kind == "mail.flags" })
	if ev.Mailbox != "INBOX" || ev.UID == nil || *ev.UID != 1 {
		t.Fatalf("mail.flags event: %+v", ev)
	}
	t.Log("✔ flag change → mail.flags")

	ijson(t, srv, "POST", "/internal/v1/message/batch", acct, map[string]any{"idList": []string{id}, "action": "delete"})
	ev = waitFor("mail.expunge", func(e webhook.Event) bool { return e.Kind == "mail.expunge" })
	if ev.Mailbox != "INBOX" || ev.UID == nil || *ev.UID != 1 {
		t.Fatalf("mail.expunge event: %+v", ev)
	}
	waitFor("mail.new Trash", func(e webhook.Event) bool { return e.Kind == "mail.new" && e.Mailbox == "Trash" })
	mu.Lock()
	defer mu.Unlock()
	if badSig != 0 {
		t.Fatalf("%d webhook requests failed signature/id checks", badSig)
	}
	t.Log("✔ move to Trash → mail.expunge INBOX + mail.new Trash, all signatures valid")
}

// TestInternalAppPassword: an account's own app passwords through the service
// token (ara's mail setting). Issue shows the plain text once, list has it,
// another account can neither see nor revoke it.
func TestInternalAppPassword(t *testing.T) {
	srv, st := internalServer(t, testServiceToken)
	if _, err := st.CreateDomain(context.Background(), "ara.test"); err != nil {
		t.Fatalf("domain: %v", err)
	}
	acctA, _ := provisionAs(t, srv, "ara:a", "alpha@ara.test")
	acctB, _ := provisionAs(t, srv, "ara:b", "beta@ara.test")

	code, made := ijson(t, srv, "POST", "/internal/v1/app-password", acctA, map[string]any{"label": "thunderbird"})
	if code != http.StatusCreated || made["plaintext"] == "" || made["plaintext"] == nil {
		t.Fatalf("issue: %d %v", code, made)
	}
	id := made["appPassword"].(map[string]any)["id"].(string)

	code, raw := icall(t, srv, "GET", "/internal/v1/app-password", testServiceToken, acctA, nil)
	if code != 200 || !strings.Contains(string(raw), id) || strings.Contains(string(raw), made["plaintext"].(string)) {
		t.Fatalf("A's list: %d %s", code, raw)
	}
	_, raw = icall(t, srv, "GET", "/internal/v1/app-password", testServiceToken, acctB, nil)
	if strings.Contains(string(raw), id) {
		t.Fatalf("B must not see A's app password: %s", raw)
	}
	if code, _ := icall(t, srv, "DELETE", "/internal/v1/app-password/"+id, testServiceToken, acctB, nil); code == http.StatusNoContent || code == 200 {
		t.Fatalf("B revoked A's app password: %d", code)
	}
	if code, _ := icall(t, srv, "DELETE", "/internal/v1/app-password/"+id, testServiceToken, acctA, nil); code != http.StatusNoContent && code != 200 {
		t.Fatalf("A revoke: %d", code)
	}
	if code, _ := icall(t, srv, "GET", "/internal/v1/app-password", "", acctA, nil); code != 401 {
		t.Fatalf("no service token must be 401, got %d", code)
	}
	t.Log("✔ own app passwords issued once, listed without the secret, not crossable, revoked")
}
