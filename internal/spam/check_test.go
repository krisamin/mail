package spam

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestReverseIP(t *testing.T) {
	caseMap := map[string]string{
		"1.2.3.4":     "4.3.2.1",
		"127.0.0.2":   "2.0.0.127",
		"192.168.1.9": "9.1.168.192",
	}
	for in, want := range caseMap {
		if got := reverseIP(net.ParseIP(in)); got != want {
			t.Fatalf("reverseIP(%s) = %q, want %q", in, got, want)
		}
	}
	// IPv6 nibble reversal (RFC 5782 example: 2001:db8::1)
	got := reverseIP(net.ParseIP("2001:db8::1"))
	want := "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2"
	if got != want {
		t.Fatalf("reverseIP(2001:db8::1) = %q, want %q", got, want)
	}
	t.Log("✔ DNSBL reverse forms")
}

func TestPlausibleHELO(t *testing.T) {
	okList := []string{"mail.example.com", "mx1.google.com", "a.b"}
	badList := []string{"", "localhost", "WIN-ABC123", "1.2.3.4", "[1.2.3.4]", "2001:db8::1"}
	for _, h := range okList {
		if !plausibleHELO(h) {
			t.Fatalf("plausibleHELO(%q) should be true", h)
		}
	}
	for _, h := range badList {
		if plausibleHELO(h) {
			t.Fatalf("plausibleHELO(%q) should be false", h)
		}
	}
	t.Log("✔ HELO plausibility")
}

func TestCheckDNSBL(t *testing.T) {
	c := NewChecker([]string{"zen.spamhaus.org"})
	queried := map[string]bool{}
	c.lookupHost = func(_ context.Context, host string) ([]string, error) {
		queried[host] = true
		switch host {
		case "2.0.0.127.zen.spamhaus.org":
			return []string{"127.0.0.4"}, nil // listed
		case "3.0.0.127.zen.spamhaus.org":
			return []string{"127.255.255.254"}, nil // blocklist error code
		default:
			return nil, errors.New("NXDOMAIN")
		}
	}

	// listed
	v := c.CheckDNSBL(context.Background(), net.ParseIP("127.0.0.2"))
	// 127.0.0.2 is loopback — private IPs skip. Use a public-looking test IP
	// by faking through the resolver instead:
	_ = v

	// swap to public IPs mapped onto the fake zone answers
	c2 := NewChecker([]string{"zen.example.test"})
	c2.lookupHost = func(_ context.Context, host string) ([]string, error) {
		switch host {
		case "4.3.2.1.zen.example.test":
			return []string{"127.0.0.4"}, nil // listed
		case "5.3.2.1.zen.example.test":
			return []string{"127.255.255.254"}, nil // error code → unavailable
		case "6.3.2.1.zen.example.test":
			return []string{"10.0.0.1"}, nil // non-127 garbage → not listed
		default:
			return nil, errors.New("NXDOMAIN")
		}
	}

	if v := c2.CheckDNSBL(context.Background(), net.ParseIP("1.2.3.4")); !v.Listed || v.Code != "127.0.0.4" {
		t.Fatalf("1.2.3.4 should be listed: %+v", v)
	}
	if v := c2.CheckDNSBL(context.Background(), net.ParseIP("1.2.3.5")); v.Listed {
		t.Fatalf("blocklist error code must not count as listed: %+v", v)
	}
	if v := c2.CheckDNSBL(context.Background(), net.ParseIP("1.2.3.6")); v.Listed {
		t.Fatalf("non-127 answer must not count as listed: %+v", v)
	}
	if v := c2.CheckDNSBL(context.Background(), net.ParseIP("1.2.3.7")); v.Listed {
		t.Fatalf("NXDOMAIN must not count as listed: %+v", v)
	}

	// cache: second lookup for the same IP must not re-query
	callCount := 0
	c3 := NewChecker([]string{"zen.example.test"})
	c3.lookupHost = func(_ context.Context, host string) ([]string, error) {
		callCount++
		return []string{"127.0.0.4"}, nil
	}
	c3.CheckDNSBL(context.Background(), net.ParseIP("9.9.9.9"))
	c3.CheckDNSBL(context.Background(), net.ParseIP("9.9.9.9"))
	if callCount != 1 {
		t.Fatalf("cache miss: %d lookups for the same IP", callCount)
	}

	// private IP skips entirely
	if v := c2.CheckDNSBL(context.Background(), net.ParseIP("192.168.0.10")); v.Listed {
		t.Fatalf("private IP must be skipped: %+v", v)
	}
	t.Log("✔ DNSBL listed/error-code/cache/private handling")
}

func TestCheckConnection(t *testing.T) {
	c := NewChecker(nil)
	c.lookupAddr = func(_ context.Context, addr string) ([]string, error) {
		switch addr {
		case "1.2.3.4":
			return []string{"mail.good.example."}, nil
		case "1.2.3.5":
			return []string{"mail.liar.example."}, nil // PTR exists, forward mismatches
		default:
			return nil, errors.New("no PTR")
		}
	}
	c.lookupHost = func(_ context.Context, host string) ([]string, error) {
		switch host {
		case "mail.good.example":
			return []string{"1.2.3.4"}, nil
		case "mail.liar.example":
			return []string{"9.9.9.9"}, nil
		default:
			return nil, errors.New("NXDOMAIN")
		}
	}

	// FCrDNS pass + good HELO
	v := c.CheckConnection(context.Background(), net.ParseIP("1.2.3.4"), "mail.good.example")
	if !v.RDNSOk || v.RDNSName != "mail.good.example" || v.Suspicious {
		t.Fatalf("FCrDNS pass expected: %+v", v)
	}

	// PTR forward mismatch + bad HELO → suspicious
	v = c.CheckConnection(context.Background(), net.ParseIP("1.2.3.5"), "localhost")
	if v.RDNSOk || !v.Suspicious {
		t.Fatalf("forward mismatch + bad HELO should be suspicious: %+v", v)
	}

	// no PTR but plausible HELO → not suspicious (single weak signal)
	v = c.CheckConnection(context.Background(), net.ParseIP("1.2.3.9"), "mail.small.example")
	if v.RDNSOk || v.Suspicious {
		t.Fatalf("single weak signal should not be suspicious: %+v", v)
	}

	// private IP → always ok
	v = c.CheckConnection(context.Background(), net.ParseIP("10.0.0.5"), "whatever")
	if !v.RDNSOk || v.Suspicious {
		t.Fatalf("private IP should skip rDNS screening: %+v", v)
	}
	t.Log("✔ FCrDNS / HELO / suspicious combination")
}
