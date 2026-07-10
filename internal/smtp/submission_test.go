package smtp

import (
	"net"
	"strings"
	"testing"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"
)

// Submission tests. Requires dev Postgres (reuses setupServers from e2e_test.go).

func setupSubmission(t *testing.T) (*testEnv, string) {
	t.Helper()
	env := setupServers(t)

	subSrv := gosmtp.NewServer(NewSubmissionBackend(env.store, "submit-test.krisam.in", false))
	subSrv.Domain = "submit-test.krisam.in"
	subSrv.AllowInsecureAuth = true
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("submission listen: %v", err)
	}
	go func() { _ = subSrv.Serve(ln) }()
	t.Cleanup(func() { _ = subSrv.Close() })

	return env, ln.Addr().String()
}

const submitMessage = "From: Maro <maro@krisam.in>\r\n" +
	"To: Shiro <shiro@krisam.in>\r\n" +
	"Subject: submitted mail\r\n" +
	"\r\n" +
	"sent by an authenticated user.\r\n"

func dialSubmission(t *testing.T, addr string) *gosmtp.Client {
	t.Helper()
	c, err := gosmtp.Dial(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Hello("client.example.com"); err != nil {
		t.Fatalf("HELO: %v", err)
	}
	return c
}

// TestSubmissionRequiresAuth: MAIL FROM without AUTH is rejected.
func TestSubmissionRequiresAuth(t *testing.T) {
	_, subAddr := setupSubmission(t)
	c := dialSubmission(t, subAddr)

	err := c.Mail(testAddr, nil)
	if err == nil {
		t.Fatal("MAIL passed without AUTH")
	}
	t.Logf("✔ unauthenticated MAIL rejected: %v", err)
}

// TestSubmissionAuthFailure: a wrong app password yields 535.
func TestSubmissionAuthFailure(t *testing.T) {
	_, subAddr := setupSubmission(t)
	c := dialSubmission(t, subAddr)

	err := c.Auth(sasl.NewPlainClient("", testAddr, "wrong-password"))
	if err == nil {
		t.Fatal("AUTH passed with a wrong password")
	}
	t.Logf("✔ AUTH failure: %v", err)
}

// TestSubmissionSenderSpoofing: an envelope from differing from the authenticated account yields 553.
func TestSubmissionSenderSpoofing(t *testing.T) {
	_, subAddr := setupSubmission(t)
	c := dialSubmission(t, subAddr)

	if err := c.Auth(sasl.NewPlainClient("", testAddr, testPass)); err != nil {
		t.Fatalf("AUTH: %v", err)
	}
	err := c.Mail("someone-else@krisam.in", nil)
	if err == nil {
		t.Fatal("sender spoofing passed")
	}
	smtpErr, ok := err.(*gosmtp.SMTPError)
	if !ok || smtpErr.Code != 553 {
		t.Fatalf("should be 553: %v", err)
	}
	t.Logf("✔ sender spoofing rejected with 553: %v", err)
}

// TestSubmissionExternalDomainRejected: external domains are rejected until the outbound queue exists.
func TestSubmissionExternalDomainRejected(t *testing.T) {
	_, subAddr := setupSubmission(t)
	c := dialSubmission(t, subAddr)

	if err := c.Auth(sasl.NewPlainClient("", testAddr, testPass)); err != nil {
		t.Fatalf("AUTH: %v", err)
	}
	if err := c.Mail(testAddr, nil); err != nil {
		t.Fatalf("MAIL: %v", err)
	}
	err := c.Rcpt("friend@gmail.com", nil)
	if err == nil {
		t.Fatal("external domain accepted (without an outbound queue)")
	}
	t.Logf("✔ external domain rejected (pre Phase 2-3): %v", err)
}

// TestSubmissionLocalDelivery: authenticated user submits to a local user → verified via IMAP.
func TestSubmissionLocalDelivery(t *testing.T) {
	env, subAddr := setupSubmission(t)
	c := dialSubmission(t, subAddr)

	if err := c.Auth(sasl.NewPlainClient("", testAddr, testPass)); err != nil {
		t.Fatalf("AUTH: %v", err)
	}
	if err := c.Mail(testAddr, nil); err != nil {
		t.Fatalf("MAIL: %v", err)
	}
	if err := c.Rcpt(testAddr2, nil); err != nil {
		t.Fatalf("RCPT: %v", err)
	}
	w, err := c.Data()
	if err != nil {
		t.Fatalf("DATA: %v", err)
	}
	if _, err := w.Write([]byte(submitMessage)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_ = c.Quit()

	// check shiro's INBOX (IMAP)
	messageList := readInbox(t, env.imapAddr, testAddr2, testPass)
	if len(messageList) != 1 {
		t.Fatalf("shiro INBOX should have 1 message: %d", len(messageList))
	}
	if messageList[0].Envelope.Subject != "submitted mail" {
		t.Fatalf("unexpected subject: %+v", messageList[0].Envelope)
	}
	full := string(messageList[0].BodySection[0].Bytes)
	if !strings.Contains(full, "for <"+testAddr2+">") {
		t.Fatalf("missing Received header:\n%.200s", full)
	}
	t.Logf("✔ authenticated submission → local delivery → IMAP verified: %q", messageList[0].Envelope.Subject)
}
