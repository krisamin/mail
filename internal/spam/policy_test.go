package spam

import (
	"context"
	"errors"
	"testing"
)

func fp(v float64) *float64 { return &v }

func TestPolicyDecide(t *testing.T) {
	caseList := []struct {
		name string
		p    Policy
		r    ScanResult
		want ScanAction
	}{
		{"no threshold keeps rspamd reject", Policy{}, ScanResult{Action: ScanReject, Score: 20}, ScanReject},
		{"no threshold keeps rspamd junk", Policy{}, ScanResult{Action: ScanJunk, Score: 7}, ScanJunk},
		{"reject score reached", Policy{RejectScore: fp(10)}, ScanResult{Action: ScanPass, Score: 10}, ScanReject},
		{"rspamd reject below reject score is junk", Policy{RejectScore: fp(30)}, ScanResult{Action: ScanReject, Score: 16}, ScanJunk},
		{"junk score reached", Policy{JunkScore: fp(4)}, ScanResult{Action: ScanPass, Score: 4.5}, ScanJunk},
		{"rspamd junk below junk score passes", Policy{JunkScore: fp(9)}, ScanResult{Action: ScanJunk, Score: 6}, ScanPass},
		{"both, middle", Policy{JunkScore: fp(5), RejectScore: fp(12)}, ScanResult{Action: ScanReject, Score: 8}, ScanJunk},
		{"both, low", Policy{JunkScore: fp(5), RejectScore: fp(12)}, ScanResult{Action: ScanJunk, Score: 2}, ScanPass},
	}
	for _, c := range caseList {
		if got := c.p.Decide(c.r); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestPolicyNormalize(t *testing.T) {
	p, err := Policy{GreylistDelaySecond: 60, DNSBLZoneList: []string{" Zen.Spamhaus.org ", "", "zen.spamhaus.org"}}.Normalize()
	if err != nil || len(p.DNSBLZoneList) != 1 || p.DNSBLZoneList[0] != "zen.spamhaus.org" {
		t.Fatalf("normalize: %v %v", p, err)
	}
	for _, bad := range []Policy{
		{GreylistDelaySecond: 0},
		{GreylistDelaySecond: 3601},
		{GreylistDelaySecond: 60, DNSBLZoneList: []string{"localhost"}},
		{GreylistDelaySecond: 60, JunkScore: fp(-1)},
		{GreylistDelaySecond: 60, JunkScore: fp(5), RejectScore: fp(5)},
	} {
		if _, err := bad.Normalize(); err == nil {
			t.Errorf("want error for %+v", bad)
		}
	}
}

func TestSourceOverrideAndCache(t *testing.T) {
	saved := ""
	loadCount := 0
	var loadErr error
	load := func(ctx context.Context) (string, bool, error) {
		loadCount++
		return saved, saved != "", loadErr
	}
	built := 0
	def := Policy{Greylist: true, GreylistDelaySecond: 60, DNSBLZoneList: []string{"a.example"}, Rspamd: true}
	src := NewSource(def, NewScanner("http://rspamd.invalid", ""), load)
	src.newChecker = func(z []string) *Checker { built++; return NewChecker(z) }

	snap, isSaved := src.Current(context.Background())
	if isSaved || !snap.Greylist || snap.Checker == nil || snap.Scanner == nil || built != 1 {
		t.Fatalf("default: %+v saved=%v built=%d", snap, isSaved, built)
	}
	// cached: no second load inside the ttl
	src.Current(context.Background())
	if loadCount != 1 {
		t.Fatalf("cache: loads=%d", loadCount)
	}

	// a saved policy applies after Invalidate; same zones keep the checker
	saved = `{"greylist":false,"greylistDelaySecond":30,"dnsblZoneList":["a.example"],"rspamd":false}`
	src.Invalidate()
	snap, isSaved = src.Current(context.Background())
	if !isSaved || snap.Greylist || snap.Scanner != nil || snap.GreylistDelay.Seconds() != 30 || built != 1 {
		t.Fatalf("saved: %+v saved=%v built=%d", snap, isSaved, built)
	}

	// zones off turn the checker off; new zones build a new one
	saved = `{"greylistDelaySecond":30,"dnsblZoneList":[]}`
	src.Invalidate()
	if snap, _ = src.Current(context.Background()); snap.Checker != nil {
		t.Fatalf("zones off: checker %v", snap.Checker)
	}
	saved = `{"greylistDelaySecond":30,"dnsblZoneList":["b.example"]}`
	src.Invalidate()
	if snap, _ = src.Current(context.Background()); snap.Checker == nil || built != 2 {
		t.Fatalf("zones new: built=%d", built)
	}

	// a load error keeps the last snapshot; a broken saved value falls back to env
	loadErr = errors.New("db down")
	src.Invalidate()
	if _, isSaved = src.Current(context.Background()); !isSaved {
		t.Fatalf("load error should keep the last snapshot")
	}
	loadErr = nil
	saved = `{"greylistDelaySecond":0}`
	src.Invalidate()
	if snap, isSaved = src.Current(context.Background()); isSaved || !snap.Greylist {
		t.Fatalf("invalid saved should fall back to env: %+v", snap)
	}
}
