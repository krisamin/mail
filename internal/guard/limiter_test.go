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

func TestLimiterExponentialBlock(t *testing.T) {
	now := time.Now()
	l := NewLimiter()
	l.MaxFailure = 2
	l.Window = time.Minute
	l.BlockFor = time.Minute
	l.MaxBlock = 4 * time.Minute
	l.timeNow = func() time.Time { return now }

	trip := func() {
		l.Fail("k")
		l.Fail("k")
	}

	// 1차 차단: 1분
	trip()
	if l.Allow("k") {
		t.Fatal("1차 차단 안 됨")
	}
	now = now.Add(90 * time.Second) // 1분 차단 만료
	if !l.Allow("k") {
		t.Fatal("1차 차단(1m)이 90초 후에도 유지됨")
	}

	// 2차 차단: 2분 — 90초 후에도 유지돼야 함
	trip()
	now = now.Add(90 * time.Second)
	if l.Allow("k") {
		t.Fatal("2차 차단(2m)이 90초 만에 풀림 — 지수 증가 미동작")
	}
	now = now.Add(60 * time.Second) // 총 150초 > 2분
	if !l.Allow("k") {
		t.Fatal("2차 차단(2m) 만료됐는데 유지됨")
	}

	// 3차(4분=상한), 4차도 상한 유지
	trip()
	now = now.Add(3 * time.Minute)
	if l.Allow("k") {
		t.Fatal("3차 차단(4m 상한)이 3분 만에 풀림")
	}
	now = now.Add(2 * time.Minute)
	if !l.Allow("k") {
		t.Fatal("3차 차단(상한 4m) 만료됐는데 유지됨")
	}
	t.Log("✔ 지수 차단 1m→2m→4m(상한)")

	// 성공하면 지수도 리셋
	l.Success("k")
	trip()
	now = now.Add(90 * time.Second)
	if !l.Allow("k") {
		t.Fatal("성공 후 재차단인데 지수가 리셋 안 됨 (1m여야 함)")
	}
	t.Log("✔ 성공 시 지수 리셋")
}

func TestKeyForIP(t *testing.T) {
	caseMap := map[string]string{
		"1.2.3.4":                "1.2.3.4",
		"::ffff:1.2.3.4":         "1.2.3.4",
		"2001:db8:abcd:12::1":    "2001:db8:abcd:12::/64",
		"2001:db8:abcd:12::beef": "2001:db8:abcd:12::/64",
		"2001:db8:abcd:13::1":    "2001:db8:abcd:13::/64",
		"not-an-ip":              "not-an-ip",
	}
	for in, want := range caseMap {
		if got := KeyForIP(in); got != want {
			t.Fatalf("KeyForIP(%q) = %q, want %q", in, got, want)
		}
	}
	// 같은 /64의 다른 두 주소는 같은 키
	if KeyForIP("2001:db8:abcd:12::1") != KeyForIP("2001:db8:abcd:12:ffff::2") {
		t.Fatal("같은 /64인데 키가 다름")
	}
	t.Log("✔ IPv6 /64 정규화")
}
