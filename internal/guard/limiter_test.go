package guard

import (
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	now := time.Now()
	l := NewLimiter()
	l.MaxFailure = 3
	l.Window = time.Minute
	l.BlockFor = time.Minute
	l.timeNow = func() time.Time { return now }

	// 임계치 전까지 허용
	for i := 0; i < 2; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("실패 %d회에 차단되면 안 됨", i)
		}
		l.Fail("1.2.3.4")
	}
	if !l.Allow("1.2.3.4") {
		t.Fatal("임계치 전인데 차단됨")
	}

	// 3번째 실패 → 차단
	l.Fail("1.2.3.4")
	if l.Allow("1.2.3.4") {
		t.Fatal("임계치 도달인데 허용됨")
	}
	// 다른 IP는 무관
	if !l.Allow("5.6.7.8") {
		t.Fatal("다른 키가 차단됨")
	}
	t.Log("✔ 임계치 도달 시 차단 + 키 격리")

	// 차단 시간 경과 → 해제
	now = now.Add(2 * time.Minute)
	if !l.Allow("1.2.3.4") {
		t.Fatal("차단 만료됐는데 여전히 차단")
	}
	t.Log("✔ 차단 만료 후 해제")

	// 성공 → 기록 삭제
	l.Fail("1.2.3.4")
	l.Success("1.2.3.4")
	for i := 0; i < 2; i++ {
		l.Fail("1.2.3.4")
	}
	if !l.Allow("1.2.3.4") {
		t.Fatal("성공으로 리셋됐는데 이전 실패가 카운트됨")
	}
	t.Log("✔ 성공 시 실패 기록 리셋")

	// 빈 키는 항상 허용
	if !l.Allow("") {
		t.Fatal("빈 키 차단")
	}
}
