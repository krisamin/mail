package smtp

import (
	"context"
	"strings"
	"testing"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"
)

// 별칭 + 멀티도메인 내부 라우팅 e2e.
//
// 마로 시나리오: 서버에 krisam.in과 kirby.so가 둘 다 있으면
// 둘 사이 메일은 relay(Resend)를 안 거치고 내부 배달돼야 한다.
// 여기에 별칭(hello@)과 catch-all(*@kirby.so)까지 검증.

// TestAliasDelivery: MX 수신 경로에서 별칭/와일드카드로 배달.
func TestAliasDelivery(t *testing.T) {
	env := setupServers(t)
	ctx := context.Background()

	// 시드: kirby.so 도메인 + 별칭 2개 (maro 유저에게)
	var kirbyID int64
	if err := env.store.Pool().QueryRow(ctx,
		`INSERT INTO domain (name) VALUES ('kirby.so') RETURNING id`).Scan(&kirbyID); err != nil {
		t.Fatalf("kirby.so 시드: %v", err)
	}
	maro, err := env.store.FindAccountByAddress(ctx, testAddr)
	if err != nil {
		t.Fatalf("maro 조회: %v", err)
	}
	var krisamID int64 = maro.DomainID
	if _, err := env.store.CreateAlias(ctx, krisamID, "hello", maro.ID); err != nil {
		t.Fatalf("정확 별칭: %v", err)
	}
	if _, err := env.store.CreateAlias(ctx, kirbyID, "*", maro.ID); err != nil {
		t.Fatalf("catch-all: %v", err)
	}

	// 1) 정확 별칭 hello@krisam.in으로 수신 → maro INBOX
	if err := sendSMTP(t, env.smtpAddr, "ext@example.com", []string{"hello@krisam.in"},
		"From: ext@example.com\r\nTo: hello@krisam.in\r\nSubject: to alias\r\n\r\nalias mail\r\n"); err != nil {
		t.Fatalf("별칭 수신: %v", err)
	}

	// 2) catch-all: 아무 주소나 @kirby.so → maro INBOX
	if err := sendSMTP(t, env.smtpAddr, "ext@example.com", []string{"whatever-12345@kirby.so"},
		"From: ext@example.com\r\nTo: whatever-12345@kirby.so\r\nSubject: to catchall\r\n\r\ncatchall mail\r\n"); err != nil {
		t.Fatalf("catch-all 수신: %v", err)
	}

	// 3) 별칭 없는 주소는 여전히 550
	if err := trySend(env.smtpAddr, "ext@example.com", "nobody@krisam.in"); err == nil {
		t.Fatal("nobody@krisam.in이 수락됨 (550이어야)")
	} else if !strings.Contains(err.Error(), "550") {
		t.Fatalf("550이어야: %v", err)
	}

	// maro INBOX에서 두 통 다 확인
	messageList := readInbox(t, env.imapAddr, testAddr, testPass)
	var subjects []string
	for _, m := range messageList {
		if m.Envelope != nil {
			subjects = append(subjects, m.Envelope.Subject)
		}
	}
	for _, want := range []string{"to alias", "to catchall"} {
		found := false
		for _, s := range subjects {
			if strings.Contains(s, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("INBOX에 %q 없음: %v", want, subjects)
		}
	}
	t.Logf("✔ 별칭 + catch-all 배달 확인 (INBOX %d통): %v", len(subjects), subjects)
	t.Log("✔ 미등록 주소 550 유지")
}

// TestInternalRoutingTwoDomains: 우리 서버의 두 도메인 간 제출은
// 발송 큐(relay)를 거치지 않고 내부 배달된다.
func TestInternalRoutingTwoDomains(t *testing.T) {
	env, subAddr := setupSubmission(t) // enqueueEnabled=false — relay 없는 구성
	ctx := context.Background()

	// kirby.so + catch-all(maro)
	var kirbyID int64
	if err := env.store.Pool().QueryRow(ctx,
		`INSERT INTO domain (name) VALUES ('kirby.so') RETURNING id`).Scan(&kirbyID); err != nil {
		t.Fatalf("kirby.so 시드: %v", err)
	}
	maro, _ := env.store.FindAccountByAddress(ctx, testAddr)
	if _, err := env.store.CreateAlias(ctx, kirbyID, "*", maro.ID); err != nil {
		t.Fatalf("catch-all: %v", err)
	}

	// shiro가 인증하고 team@kirby.so로 제출 —
	// kirby.so는 우리 도메인이므로 큐 비활성이어도 배달돼야 한다 (내부 라우팅)
	c := dialSubmission(t, subAddr)
	if err := c.Auth(sasl.NewPlainClient("", testAddr2, testPass)); err != nil {
		t.Fatalf("AUTH: %v", err)
	}
	if err := c.Mail(testAddr2, nil); err != nil {
		t.Fatalf("MAIL: %v", err)
	}
	if err := c.Rcpt("team@kirby.so", nil); err != nil {
		t.Fatalf("RCPT team@kirby.so (내부 도메인인데 거절됨): %v", err)
	}
	w, err := c.Data()
	if err != nil {
		t.Fatalf("DATA: %v", err)
	}
	msg := "From: shiro@krisam.in\r\nTo: team@kirby.so\r\nSubject: cross-domain internal\r\n\r\ninternal routing!\r\n"
	if _, err := w.Write([]byte(msg)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 발송 큐는 비어있어야 (relay 안 거침)
	var queued int
	_ = env.store.Pool().QueryRow(ctx, `SELECT count(*) FROM outbound_queue`).Scan(&queued)
	if queued != 0 {
		t.Fatalf("내부 라우팅인데 큐에 %d건 들어감", queued)
	}

	// maro INBOX에 도착 확인 (catch-all이 maro니까)
	messageList := readInbox(t, env.imapAddr, testAddr, testPass)
	found := false
	for _, m := range messageList {
		if m.Envelope != nil && strings.Contains(m.Envelope.Subject, "cross-domain internal") {
			found = true
		}
	}
	if !found {
		t.Fatalf("내부 배달 안 됨 (%d통)", len(messageList))
	}
	t.Log("✔ krisam.in → kirby.so 제출이 relay 없이 내부 배달 (큐 0건)")
}

// TestSubmissionSendAsAlias: 별칭 주소를 envelope from으로 발신 가능,
// 남의 별칭은 553.
func TestSubmissionSendAsAlias(t *testing.T) {
	env, subAddr := setupSubmission(t)
	ctx := context.Background()

	maro, _ := env.store.FindAccountByAddress(ctx, testAddr)
	if _, err := env.store.CreateAlias(ctx, maro.DomainID, "hello", maro.ID); err != nil {
		t.Fatalf("별칭: %v", err)
	}

	// maro가 hello@krisam.in으로 발신 → 허용
	c := dialSubmission(t, subAddr)
	if err := c.Auth(sasl.NewPlainClient("", testAddr, testPass)); err != nil {
		t.Fatalf("AUTH: %v", err)
	}
	if err := c.Mail("hello@krisam.in", nil); err != nil {
		t.Fatalf("본인 별칭 발신이 거절됨: %v", err)
	}
	t.Log("✔ 본인 별칭으로 MAIL FROM 허용")

	// shiro가 hello@krisam.in으로 발신 → 553
	c2 := dialSubmission(t, subAddr)
	if err := c2.Auth(sasl.NewPlainClient("", testAddr2, testPass)); err != nil {
		t.Fatalf("AUTH2: %v", err)
	}
	err := c2.Mail("hello@krisam.in", nil)
	if err == nil {
		t.Fatal("남의 별칭 발신이 허용됨")
	}
	if !strings.Contains(err.Error(), "553") {
		t.Fatalf("553이어야: %v", err)
	}
	t.Logf("✔ 타인 별칭 발신 553: %v", err)
}

// trySend는 한 통 보내기를 시도하고 RCPT 단계 에러를 돌려준다.
func trySend(smtpAddr, from, to string) error {
	c, err := gosmtp.Dial(smtpAddr)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Hello("test.example.com"); err != nil {
		return err
	}
	if err := c.Mail(from, nil); err != nil {
		return err
	}
	return c.Rcpt(to, nil)
}
