package smtp

import (
	"strings"
	"testing"
)

// DMARC 정책 집행 e2e — 가짜 DNS 없이 실 DNS로는 재현이 어려워
// (테스트용 도메인의 DMARC 레코드 필요) 백엔드 플래그 동작만 검증한다.
// 정책 판정 자체(auth.VerifyInbound의 DMARCEvaluated/DMARCPolicy)는
// internal/auth 테스트가 가짜 DNS로 커버한다.

// TestDMARCEnforcementFlag: WithDMARCEnforcement가 검증까지 켜는지.
func TestDMARCEnforcementFlag(t *testing.T) {
	b := NewBackend(nil, "mx.test").WithDMARCEnforcement()
	if !b.verifyInbound || !b.enforceDMARC {
		t.Fatal("WithDMARCEnforcement는 verifyInbound+enforceDMARC 둘 다 켜야")
	}
	b2 := NewBackend(nil, "mx.test").WithInboundVerification()
	if !b2.verifyInbound || b2.enforceDMARC {
		t.Fatal("WithInboundVerification은 검증만 켜야")
	}
	t.Log("✔ 백엔드 플래그")
}

// TestJunkFolderDelivery: quarantine 판정 시 Junk 폴더로 배달되는 경로 —
// deliver가 임의 폴더를 자동 생성하는지 확인.
func TestJunkFolderDelivery(t *testing.T) {
	env := setupServers(t)

	// deliver를 직접 호출해 Junk 폴더 생성+배달 검증
	sess := &Session{backend: &Backend{store: env.store, hostname: "mx.test"}}
	maro, err := env.store.FindAccountByAddress(t.Context(), testAddr)
	if err != nil {
		t.Fatalf("maro: %v", err)
	}
	raw := []byte("From: spam@example.com\r\nTo: " + testAddr + "\r\nSubject: junk test\r\n\r\nspammy\r\n")
	if err := sess.deliver(rcpt{address: testAddr, user: maro}, "Junk", raw, timeNow()); err != nil {
		t.Fatalf("Junk 배달: %v", err)
	}

	// Junk 폴더가 생겼고 메일이 들어있는지
	junk, err := env.store.GetMailbox(t.Context(), maro.ID, "Junk")
	if err != nil {
		t.Fatalf("Junk 폴더 없음: %v", err)
	}
	messageList, err := env.store.ListMessages(t.Context(), junk.ID)
	if err != nil || len(messageList) != 1 {
		t.Fatalf("Junk 메일 1통이어야: %v %d", err, len(messageList))
	}
	if !strings.Contains(messageList[0].Subject, "junk test") {
		t.Fatalf("제목 이상: %q", messageList[0].Subject)
	}
	t.Log("✔ Junk 폴더 자동 생성 + 배달")
}
