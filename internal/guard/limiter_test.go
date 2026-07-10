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

	// allowed until the threshold
	for i := 0; i < 2; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("must not block at %d failures", i)
		}
		l.Fail("1.2.3.4")
	}
	if !l.Allow("1.2.3.4") {
		t.Fatal("blocked before reaching the threshold")
	}

	// 3rd failure → block
	l.Fail("1.2.3.4")
	if l.Allow("1.2.3.4") {
		t.Fatal("allowed despite hitting the threshold")
	}
	// other IPs unaffected
	if !l.Allow("5.6.7.8") {
		t.Fatal("a different key got blocked")
	}
	t.Log("✔ block on threshold + key isolation")

	// block duration elapsed → unblocked
	now = now.Add(2 * time.Minute)
	if !l.Allow("1.2.3.4") {
		t.Fatal("still blocked after block expiry")
	}
	t.Log("✔ unblocked after expiry")

	// success → record cleared
	l.Fail("1.2.3.4")
	l.Success("1.2.3.4")
	for i := 0; i < 2; i++ {
		l.Fail("1.2.3.4")
	}
	if !l.Allow("1.2.3.4") {
		t.Fatal("prior failures counted despite success reset")
	}
	t.Log("✔ failure record reset on success")

	// empty key is always allowed
	if !l.Allow("") {
		t.Fatal("empty key blocked")
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

	// 1st block: 1 minute
	trip()
	if l.Allow("k") {
		t.Fatal("1st block did not engage")
	}
	now = now.Add(90 * time.Second) // 1-minute block expires
	if !l.Allow("k") {
		t.Fatal("1st block (1m) still held after 90s")
	}

	// 2nd block: 2 minutes — must still hold after 90s
	trip()
	now = now.Add(90 * time.Second)
	if l.Allow("k") {
		t.Fatal("2nd block (2m) released after only 90s — exponential growth inactive")
	}
	now = now.Add(60 * time.Second) // 150s total > 2 minutes
	if !l.Allow("k") {
		t.Fatal("2nd block (2m) held past expiry")
	}

	// 3rd block (4m = cap); later blocks stay at the cap
	trip()
	now = now.Add(3 * time.Minute)
	if l.Allow("k") {
		t.Fatal("3rd block (4m cap) released after only 3m")
	}
	now = now.Add(2 * time.Minute)
	if !l.Allow("k") {
		t.Fatal("3rd block (4m cap) held past expiry")
	}
	t.Log("✔ exponential blocks 1m→2m→4m (cap)")

	// success resets the exponent too
	l.Success("k")
	trip()
	now = now.Add(90 * time.Second)
	if !l.Allow("k") {
		t.Fatal("re-block after success kept the old exponent (should be 1m)")
	}
	t.Log("✔ exponent reset on success")
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
	// two different addresses in the same /64 share a key
	if KeyForIP("2001:db8:abcd:12::1") != KeyForIP("2001:db8:abcd:12:ffff::2") {
		t.Fatal("same /64 produced different keys")
	}
	t.Log("✔ IPv6 /64 normalization")
}
