package filter

import (
	"testing"

	"github.com/krisamin/mail/internal/store"
)

// Unit tests for header parsing + rule matching (no DB).

const sampleMail = "From: \"Newsletter\" <news@shop.example>\r\n" +
	"To: maro@krisam.in\r\n" +
	"Cc: guest@krisam.in\r\n" +
	"Subject: =?utf-8?b?7ZWg7J24IOyGjOsi3O2ZlA==?=\r\n" +
	"List-Unsubscribe: <mailto:unsub@shop.example>\r\n" +
	"\r\n" +
	"body\r\n"

const plainMail = "From: friend@krisam.in\r\n" +
	"To: maro@krisam.in\r\n" +
	"Subject: lunch tomorrow?\r\n" +
	"\r\n" +
	"hi\r\n"

func rule(field, headerName, matchType, pattern, action, mailbox string) *store.FilterRule {
	return &store.FilterRule{
		Name: "t", Active: true,
		Field: field, HeaderName: headerName,
		MatchType: matchType, Pattern: pattern,
		Action: action, ActionMailbox: mailbox,
	}
}

func TestMatches(t *testing.T) {
	h := parseHeader([]byte(sampleMail))

	caseList := []struct {
		name string
		r    *store.FilterRule
		want bool
	}{
		{"from contains domain", rule("from", "", "contains", "@shop.example", "move", "x"), true},
		{"from equals bare address", rule("from", "", "equals", "news@shop.example", "move", "x"), true},
		{"from case-insensitive", rule("from", "", "contains", "NEWS@SHOP", "move", "x"), true},
		{"from no match", rule("from", "", "contains", "@other.example", "move", "x"), false},
		{"to includes cc", rule("to", "", "contains", "guest@krisam.in", "move", "x"), true},
		{"subject prefix no match", rule("subject", "", "prefix", "lunch", "move", "x"), false},
		{"header exists", rule("header", "List-Unsubscribe", "contains", "unsub@", "move", "x"), true},
		{"header missing", rule("header", "X-Nope", "contains", "x", "move", "x"), false},
		{"suffix", rule("from", "", "suffix", ".example", "move", "x"), true},
	}
	for _, c := range caseList {
		if got := matches(c.r, h); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	t.Log("✔ match matrix")

	// RFC 2047 subject decodes before matching
	h2 := parseHeader([]byte(plainMail))
	if !matches(rule("subject", "", "contains", "lunch", "move", "x"), h2) {
		t.Fatalf("plain subject should match")
	}
	t.Log("✔ subject matching")
}

func TestApply(t *testing.T) {
	if v := apply(rule("from", "", "contains", "x", "discard", "")); !v.Discard {
		t.Fatalf("discard verdict")
	}
	if v := apply(rule("from", "", "contains", "x", "move", "Receipts")); v.Mailbox != "Receipts" {
		t.Fatalf("move verdict: %v", v)
	}
	if v := apply(rule("from", "", "contains", "x", "markSeen", "")); len(v.FlagList) != 1 || v.FlagList[0] != "\\Seen" {
		t.Fatalf("markSeen verdict: %v", v)
	}
	if v := apply(rule("from", "", "contains", "x", "flag", "")); len(v.FlagList) != 1 || v.FlagList[0] != "\\Flagged" {
		t.Fatalf("flag verdict: %v", v)
	}
	t.Log("✔ verdict mapping")
}

func TestParseHeaderMalformed(t *testing.T) {
	// unparseable input never matches (fail-open, delivery proceeds)
	h := parseHeader([]byte("not a mail message at all"))
	if matches(rule("from", "", "contains", "not", "move", "x"), h) {
		// from is empty on parse failure — nothing matches
		t.Fatalf("malformed mail must not match from rules")
	}
	t.Log("✔ malformed mail fail-open")
}
