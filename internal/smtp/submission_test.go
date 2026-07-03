package smtp

import (
	"net"
	"strings"
	"testing"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"
)

// submission(제출) 테스트. dev Postgres 필요 (e2e_test.go의 setupServers 재사용).

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

// TestSubmissionRequiresAuth: AUTH 없이 MAIL FROM은 거절.
func TestSubmissionRequiresAuth(t *testing.T) {
	_, subAddr := setupSubmission(t)
	c := dialSubmission(t, subAddr)

	err := c.Mail(testAddr, nil)
	if err == nil {
		t.Fatal("AUTH 없이 MAIL이 통과됨")
	}
	t.Logf("✔ 미인증 MAIL 거절: %v", err)
}

// TestSubmissionAuthFailure: 틀린 앱 비밀번호는 535.
func TestSubmissionAuthFailure(t *testing.T) {
	_, subAddr := setupSubmission(t)
	c := dialSubmission(t, subAddr)

	err := c.Auth(sasl.NewPlainClient("", testAddr, "wrong-password"))
	if err == nil {
		t.Fatal("틀린 비번인데 AUTH 통과")
	}
	t.Logf("✔ AUTH 실패: %v", err)
}

// TestSubmissionSenderSpoofing: 인증 계정과 다른 envelope from은 553.
func TestSubmissionSenderSpoofing(t *testing.T) {
	_, subAddr := setupSubmission(t)
	c := dialSubmission(t, subAddr)

	if err := c.Auth(sasl.NewPlainClient("", testAddr, testPass)); err != nil {
		t.Fatalf("AUTH: %v", err)
	}
	err := c.Mail("someone-else@krisam.in", nil)
	if err == nil {
		t.Fatal("발신자 위조가 통과됨")
	}
	smtpErr, ok := err.(*gosmtp.SMTPError)
	if !ok || smtpErr.Code != 553 {
		t.Fatalf("553이어야: %v", err)
	}
	t.Logf("✔ 발신자 위조 553 거절: %v", err)
}

// TestSubmissionExternalDomainRejected: 외부 도메인은 발송 큐 전까지 거절.
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
		t.Fatal("외부 도메인이 수락됨 (발송 큐 없는데)")
	}
	t.Logf("✔ 외부 도메인 거절 (Phase 2-3 전): %v", err)
}

// TestSubmissionLocalDelivery: 인증 유저가 로컬 유저에게 제출 → IMAP으로 확인.
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

	// shiro의 INBOX에서 확인 (IMAP)
	msgs := readInbox(t, env.imapAddr, testAddr2, testPass)
	if len(msgs) != 1 {
		t.Fatalf("shiro INBOX에 1건 있어야: %d", len(msgs))
	}
	if msgs[0].Envelope.Subject != "submitted mail" {
		t.Fatalf("subject 이상: %+v", msgs[0].Envelope)
	}
	full := string(msgs[0].BodySection[0].Bytes)
	if !strings.Contains(full, "for <"+testAddr2+">") {
		t.Fatalf("Received 헤더 없음:\n%.200s", full)
	}
	t.Logf("✔ 인증 제출 → 로컬 배달 → IMAP 확인: %q", msgs[0].Envelope.Subject)
}
