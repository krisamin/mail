package smtp

import (
	"strings"
	"testing"
)

// DMARC policy enforcement e2e — hard to reproduce with real DNS without a
// fake DNS (would need a DMARC record on a test domain), so only the backend
// flag behavior is verified here. Policy evaluation itself
// (DMARCEvaluated/DMARCPolicy of auth.VerifyInbound) is covered by the
// internal/auth tests with a fake DNS.

// TestDMARCEnforcementFlag: whether WithDMARCEnforcement also enables verification.
func TestDMARCEnforcementFlag(t *testing.T) {
	b := NewBackend(nil, "mx.test").WithDMARCEnforcement()
	if !b.verifyInbound || !b.enforceDMARC {
		t.Fatal("WithDMARCEnforcement must enable both verifyInbound+enforceDMARC")
	}
	b2 := NewBackend(nil, "mx.test").WithInboundVerification()
	if !b2.verifyInbound || b2.enforceDMARC {
		t.Fatal("WithInboundVerification must enable verification only")
	}
	t.Log("✔ backend flags")
}

// TestJunkFolderDelivery: the path that delivers to the Junk folder on a
// quarantine verdict — verifies deliver auto-creates arbitrary folders.
func TestJunkFolderDelivery(t *testing.T) {
	env := setupServers(t)

	// call deliver directly to verify Junk folder creation + delivery
	sess := &Session{backend: &Backend{store: env.store, hostname: "mx.test"}}
	maro, err := env.store.FindAccountByAddress(t.Context(), testAddr)
	if err != nil {
		t.Fatalf("maro: %v", err)
	}
	raw := []byte("From: spam@example.com\r\nTo: " + testAddr + "\r\nSubject: junk test\r\n\r\nspammy\r\n")
	if err := sess.deliver(rcpt{address: testAddr, user: maro}, "Junk", raw, timeNow()); err != nil {
		t.Fatalf("Junk delivery: %v", err)
	}

	// verify the Junk folder was created and contains the mail
	junk, err := env.store.GetMailbox(t.Context(), maro.ID, "Junk")
	if err != nil {
		t.Fatalf("Junk folder missing: %v", err)
	}
	messageList, err := env.store.ListMessage(t.Context(), junk.ID)
	if err != nil || len(messageList) != 1 {
		t.Fatalf("Junk should have 1 mail: %v %d", err, len(messageList))
	}
	if !strings.Contains(messageList[0].Subject, "junk test") {
		t.Fatalf("unexpected subject: %q", messageList[0].Subject)
	}
	t.Log("✔ Junk folder auto-created + delivered")
}
