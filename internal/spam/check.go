// Package spam implements connection-time spam screening for the MX path:
// DNSBL lookups, reverse-DNS (FCrDNS) verification, and HELO plausibility.
//
// Policy split (deliberate):
//   - DNSBL listed → REJECT (554). Blocklists are a strong, low-false-positive
//     signal when limited to well-run zones (Spamhaus ZEN by default).
//   - Missing/broken rDNS or a bogus HELO → SUSPICIOUS signal only. Callers
//     quarantine to Junk instead of rejecting — small legitimate servers do
//     occasionally have sloppy rDNS, and RFC 5321 §4.1.4 forbids rejecting
//     solely on HELO mismatch.
//
// ★DNSBL + public resolvers pitfall: Spamhaus refuses queries arriving via
// large open resolvers (1.1.1.1 / 8.8.8.8) and answers with error codes in
// 127.255.255.0/24 instead of listing data. So — unlike internal/api/dns.go,
// which intentionally queries 1.1.1.1 to see "the outside view" — DNSBL
// lookups MUST use the system resolver. Error-code answers are treated as
// "check unavailable", never as "listed" (fail-open by design: an outage of
// the blocklist must not take mail delivery down with it).
package spam

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// lookupTimeout caps a single DNS query.
const lookupTimeout = 5 * time.Second

// cacheTTL is how long a DNSBL verdict is reused for the same IP —
// retrying senders (greylisting-style reconnects) shouldn't re-query.
const cacheTTL = 15 * time.Minute

// Checker screens inbound SMTP connections.
type Checker struct {
	zoneList []string // DNSBL zones (empty = DNSBL disabled)

	// injectable for tests
	lookupHost func(ctx context.Context, host string) ([]string, error)
	lookupAddr func(ctx context.Context, addr string) ([]string, error)

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	verdict DNSBLVerdict
	at      time.Time
}

// DNSBLVerdict is the outcome of a blocklist lookup.
type DNSBLVerdict struct {
	Listed bool
	Zone   string // zone that listed the IP
	Code   string // returned A record (e.g. 127.0.0.4)
}

// ConnVerdict aggregates the connection-quality signals.
type ConnVerdict struct {
	RDNSName   string // verified FCrDNS name ("" = none)
	RDNSOk     bool   // PTR exists and forward-confirms back to the IP
	HeloOk     bool   // HELO looks like a plausible FQDN
	Suspicious bool   // combined weak-signal verdict (quarantine hint)
	SignalList []string
}

// NewChecker creates a checker. zoneList may be empty (DNSBL disabled;
// rDNS/HELO screening still works).
func NewChecker(zoneList []string) *Checker {
	var resolver net.Resolver // system resolver — see package comment
	return &Checker{
		zoneList:   zoneList,
		lookupHost: resolver.LookupHost,
		lookupAddr: resolver.LookupAddr,
		cache:      map[string]cacheEntry{},
	}
}

// CheckDNSBL looks the IP up in the configured zones. Never returns
// Listed=true for lookup errors or blocklist error codes (fail-open).
func (c *Checker) CheckDNSBL(ctx context.Context, ip net.IP) DNSBLVerdict {
	if ip == nil || len(c.zoneList) == 0 || isPrivate(ip) {
		return DNSBLVerdict{}
	}

	key := ip.String()
	c.mu.Lock()
	if e, ok := c.cache[key]; ok && time.Since(e.at) < cacheTTL {
		c.mu.Unlock()
		return e.verdict
	}
	c.mu.Unlock()

	verdict := DNSBLVerdict{}
	rev := reverseIP(ip)
	if rev != "" {
		for _, zone := range c.zoneList {
			zone = strings.TrimSpace(zone)
			if zone == "" {
				continue
			}
			lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
			addrList, err := c.lookupHost(lctx, rev+"."+zone)
			cancel()
			if err != nil {
				continue // NXDOMAIN = not listed; other errors = unavailable
			}
			for _, a := range addrList {
				code := net.ParseIP(a)
				// Only 127.0.0.0/8 answers are valid listing data; treat
				// 127.255.255.0/24 as blocklist error codes (open resolver,
				// query limit) — unavailable, not listed.
				if code == nil || code.To4() == nil || code.To4()[0] != 127 {
					continue
				}
				if code.To4()[1] == 255 && code.To4()[2] == 255 {
					continue
				}
				verdict = DNSBLVerdict{Listed: true, Zone: zone, Code: a}
				break
			}
			if verdict.Listed {
				break
			}
		}
	}

	c.mu.Lock()
	c.cache[key] = cacheEntry{verdict: verdict, at: time.Now()}
	// lazy cleanup — bound the cache
	if len(c.cache) > 10000 {
		for k, e := range c.cache {
			if time.Since(e.at) >= cacheTTL {
				delete(c.cache, k)
			}
		}
	}
	c.mu.Unlock()
	return verdict
}

// CheckConnection evaluates rDNS (FCrDNS) and HELO plausibility.
// These are weak signals — Suspicious means "quarantine", never "reject".
func (c *Checker) CheckConnection(ctx context.Context, ip net.IP, helo string) ConnVerdict {
	v := ConnVerdict{HeloOk: plausibleHELO(helo)}
	if !v.HeloOk {
		v.SignalList = append(v.SignalList, fmt.Sprintf("implausible HELO %q", helo))
	}

	if ip == nil || isPrivate(ip) {
		// local/dev traffic: rDNS is meaningless, don't hold it against them
		v.RDNSOk = true
		v.Suspicious = false
		return v
	}

	lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	nameList, err := c.lookupAddr(lctx, ip.String())
	cancel()
	if err == nil {
		for _, name := range nameList {
			// forward-confirm: the PTR name must resolve back to the IP (FCrDNS)
			fctx, fcancel := context.WithTimeout(ctx, lookupTimeout)
			addrList, ferr := c.lookupHost(fctx, strings.TrimSuffix(name, "."))
			fcancel()
			if ferr != nil {
				continue
			}
			for _, a := range addrList {
				if fip := net.ParseIP(a); fip != nil && fip.Equal(ip) {
					v.RDNSOk = true
					v.RDNSName = strings.TrimSuffix(name, ".")
					break
				}
			}
			if v.RDNSOk {
				break
			}
		}
		if !v.RDNSOk {
			v.SignalList = append(v.SignalList, "PTR exists but fails forward confirmation")
		}
	} else {
		v.SignalList = append(v.SignalList, "no PTR record")
	}

	// Both weak signals firing together is a strong hint of a bot direct-to-MX.
	v.Suspicious = !v.RDNSOk && !v.HeloOk
	return v
}

// plausibleHELO checks whether the HELO argument looks like a legitimate FQDN.
// Botnets frequently HELO with a bare word, an IP literal, or our own name.
func plausibleHELO(helo string) bool {
	helo = strings.TrimSpace(helo)
	if helo == "" {
		return false
	}
	// address-literal form "[1.2.3.4]" is protocol-legal (RFC 5321 §4.1.3)
	// but essentially never used by legitimate MTAs — treat as implausible.
	if strings.HasPrefix(helo, "[") {
		return false
	}
	if net.ParseIP(helo) != nil {
		return false // bare IP literal
	}
	if !strings.Contains(helo, ".") {
		return false // bare word ("localhost", "WIN-ABCDEF")
	}
	return true
}

// reverseIP builds the DNSBL query prefix: IPv4 octet-reversed,
// IPv6 nibble-reversed (RFC 5782).
func reverseIP(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.%d", v4[3], v4[2], v4[1], v4[0])
	}
	v6 := ip.To16()
	if v6 == nil {
		return ""
	}
	const hexDigit = "0123456789abcdef"
	b := make([]byte, 0, 63)
	for i := len(v6) - 1; i >= 0; i-- {
		b = append(b, hexDigit[v6[i]&0xF], '.', hexDigit[v6[i]>>4], '.')
	}
	return string(b[:len(b)-1])
}

// isPrivate reports loopback/RFC1918/link-local — skip screening for those.
func isPrivate(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}
