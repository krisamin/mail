package spam

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Policy is the inbound screening switch set an admin can change at runtime.
// Env gives the default; a saved policy (setting key PolicySettingKey, JSON)
// replaces it whole until it is deleted again.
type Policy struct {
	// DMARCEnforce: fail + p=reject → 550, p=quarantine → Junk.
	DMARCEnforce bool `json:"dmarcEnforce"`
	// DNSBLZoneList: empty turns connection screening (DNSBL + rDNS/HELO) off.
	DNSBLZoneList []string `json:"dnsblZoneList"`
	Greylist      bool     `json:"greylist"`
	// GreylistDelaySecond is the wait before a retried triplet passes.
	GreylistDelaySecond int `json:"greylistDelaySecond"`
	// Rspamd turns content scanning on (needs MAIL_RSPAMD_URL at boot).
	Rspamd bool `json:"rspamd"`
	// JunkScore/RejectScore, when set, decide by rspamd's score instead of
	// its own action: score >= RejectScore → 554, >= JunkScore → Junk.
	JunkScore   *float64 `json:"junkScore"`
	RejectScore *float64 `json:"rejectScore"`
}

// PolicySettingKey is the setting row that holds a saved policy.
const PolicySettingKey = "spam.policy"

var zonePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// Normalize lowercases and dedupes zones and checks every field.
func (p Policy) Normalize() (Policy, error) {
	zoneList := []string{}
	for _, z := range p.DNSBLZoneList {
		z = strings.ToLower(strings.TrimSpace(z))
		if z == "" {
			continue
		}
		if !zonePattern.MatchString(z) {
			return p, fmt.Errorf("invalid DNSBL zone %q", z)
		}
		if !slices.Contains(zoneList, z) {
			zoneList = append(zoneList, z)
		}
	}
	p.DNSBLZoneList = zoneList
	if p.GreylistDelaySecond < 1 || p.GreylistDelaySecond > 3600 {
		return p, fmt.Errorf("greylistDelaySecond must be 1..3600")
	}
	for _, sc := range []*float64{p.JunkScore, p.RejectScore} {
		if sc != nil && (*sc < 0 || *sc > 1000) {
			return p, fmt.Errorf("score must be 0..1000")
		}
	}
	if p.JunkScore != nil && p.RejectScore != nil && *p.JunkScore >= *p.RejectScore {
		return p, fmt.Errorf("junkScore must be below rejectScore")
	}
	return p, nil
}

// Decide maps a scan result through the score thresholds. With no threshold
// set, rspamd's own action stands. A set RejectScore owns rejection (rspamd's
// reject below it becomes Junk); a set JunkScore owns quarantine.
func (p Policy) Decide(r ScanResult) ScanAction {
	action := r.Action
	if p.RejectScore != nil {
		if r.Score >= *p.RejectScore {
			return ScanReject
		}
		if action == ScanReject {
			action = ScanJunk
		}
	}
	if p.JunkScore != nil {
		if r.Score >= *p.JunkScore {
			return ScanJunk
		}
		if action == ScanJunk {
			action = ScanPass
		}
	}
	return action
}

// Snapshot is the policy one SMTP session runs under, with the parts built.
type Snapshot struct {
	Policy        Policy
	Checker       *Checker // nil = connection screening off
	Scanner       *Scanner // nil = content scanning off
	Greylist      bool
	GreylistDelay time.Duration
	EnforceDMARC  bool
}

// Loader reads the saved policy JSON; found=false means none is saved.
type Loader func(ctx context.Context) (raw string, found bool, err error)

// Source hands out the current snapshot, re-reading the saved policy at most
// every ttl (and at once after Invalidate). The DNSBL checker is rebuilt only
// when the zone list changes so its cache survives.
type Source struct {
	defaultPolicy Policy
	scanner       *Scanner
	load          Loader
	ttl           time.Duration
	newChecker    func([]string) *Checker

	mu      sync.Mutex
	snap    *Snapshot
	saved   bool
	at      time.Time
	zoneKey string
	checker *Checker
}

// NewSource wires the env default, the rspamd client (nil if no URL) and the loader.
func NewSource(defaultPolicy Policy, scanner *Scanner, load Loader) *Source {
	return &Source{
		defaultPolicy: defaultPolicy, scanner: scanner, load: load,
		ttl: 15 * time.Second, newChecker: NewChecker,
	}
}

// Default is the env policy (what "reset" goes back to).
func (s *Source) Default() Policy { return s.defaultPolicy }

// RspamdAvailable says whether a scanner was configured at boot.
func (s *Source) RspamdAvailable() bool { return s.scanner != nil }

// Invalidate makes the next Current re-read the saved policy (the last
// snapshot stays as the fallback if that read fails).
func (s *Source) Invalidate() {
	s.mu.Lock()
	s.at = time.Time{}
	s.mu.Unlock()
}

// Current returns the snapshot and whether it comes from a saved policy.
func (s *Source) Current(ctx context.Context) (Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snap != nil && time.Since(s.at) < s.ttl {
		return *s.snap, s.saved
	}
	p, saved := s.defaultPolicy, false
	if s.load != nil {
		raw, found, err := s.load(ctx)
		switch {
		case err != nil:
			log.Printf("spam: policy load failed, keeping previous: %v", err)
			if s.snap != nil {
				s.at = time.Now()
				return *s.snap, s.saved
			}
		case found:
			var stored Policy
			if jerr := json.Unmarshal([]byte(raw), &stored); jerr != nil {
				log.Printf("spam: saved policy unreadable, using env default: %v", jerr)
			} else if norm, nerr := stored.Normalize(); nerr != nil {
				log.Printf("spam: saved policy invalid, using env default: %v", nerr)
			} else {
				p, saved = norm, true
			}
		}
	}
	snap := s.build(p)
	s.snap, s.saved, s.at = &snap, saved, time.Now()
	return snap, saved
}

func (s *Source) build(p Policy) Snapshot {
	key := strings.Join(p.DNSBLZoneList, ",")
	if key != s.zoneKey || (s.checker == nil && key != "") {
		s.checker = nil
		if key != "" {
			s.checker = s.newChecker(p.DNSBLZoneList)
		}
		s.zoneKey = key
	}
	snap := Snapshot{
		Policy: p, Checker: s.checker,
		Greylist: p.Greylist, GreylistDelay: time.Duration(p.GreylistDelaySecond) * time.Second,
		EnforceDMARC: p.DMARCEnforce,
	}
	if p.Rspamd {
		snap.Scanner = s.scanner
	}
	return snap
}
