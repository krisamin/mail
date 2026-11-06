package smtp

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	gosmtp "github.com/emersion/go-smtp"
)

// Domain forwarding (MAIL_FORWARD_DOMAIN): a domain this server takes on its
// MX but does not keep. One public IP carries port 25 for every domain, so a
// domain that lives on another maild (ara.ax on ara's own) is screened here
// like any inbound mail (DNSBL, greylist, rspamd, SPF/DKIM/DMARC) and then
// handed over whole, inside the same SMTP transaction, to that maild. The
// sender hears the other side's answer: a refused recipient is refused, a
// failure is 451 and the sender retries, so nothing is accepted and lost.
//
// The receiving maild trusts the hand-over (MAIL_TRUSTED_FORWARDER): from
// that network it skips the checks already made here, keeps the
// Authentication-Results this side wrote, and files the message where this
// side's verdict says (X-Maild-Folder: Junk).

// FolderHeader carries the forwarder's verdict; only a trusted forwarder's is read.
const FolderHeader = "X-Maild-Folder"

// ForwardMap is domain (lower case) → next hop host:port.
type ForwardMap map[string]string

// ParseForwardMap reads "ara.ax=maild.ara1.svc:2525,other.example=host:25".
func ParseForwardMap(spec string) (ForwardMap, error) {
	out := ForwardMap{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		domain, hop, ok := strings.Cut(part, "=")
		domain, hop = strings.ToLower(strings.TrimSpace(domain)), strings.TrimSpace(hop)
		if !ok || domain == "" || hop == "" {
			return nil, fmt.Errorf("forward entry %q: want domain=host:port", part)
		}
		if _, _, err := net.SplitHostPort(hop); err != nil {
			return nil, fmt.Errorf("forward entry %q: %w", part, err)
		}
		out[domain] = hop
	}
	return out, nil
}

// hopFor is the next hop for an address, or "" when it is kept here.
func (m ForwardMap) hopFor(address string) string {
	at := strings.LastIndex(address, "@")
	if at < 0 || m == nil {
		return ""
	}
	return m[strings.ToLower(address[at+1:])]
}

// WithForward hands the given domains' mail to another maild.
func (b *Backend) WithForward(m ForwardMap) *Backend {
	b.forward = m
	return b
}

// WithTrustedForwarder trusts hand-overs from these networks (see top).
func (b *Backend) WithTrustedForwarder(netList []*net.IPNet) *Backend {
	b.trustedForwarder = netList
	return b
}

// ParseCIDRList reads "10.0.0.0/8,192.168.0.0/16".
func ParseCIDRList(spec string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		_, n, err := net.ParseCIDR(part)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// fromTrustedForwarder: the session comes from a forwarder this maild trusts.
func (s *Session) fromTrustedForwarder() bool {
	ip := remoteIP(s.remoteAddr)
	if ip == nil {
		return false
	}
	for _, n := range s.backend.trustedForwarder {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// forwardDialTimeout bounds one hand-over; the sender is waiting on DATA.
const forwardDialTimeout = 20 * time.Second

// handOver relays one message to hop for rcptList, in one transaction.
// raw already carries this side's Received and Authentication-Results.
func (s *Session) handOver(hop string, rcptList []string, raw []byte) error {
	conn, err := net.DialTimeout("tcp", hop, forwardDialTimeout)
	if err != nil {
		return err
	}
	conn.SetDeadline(time.Now().Add(2 * time.Minute))
	c := gosmtp.NewClient(conn)
	defer c.Close()
	if err := c.Hello(s.backend.hostname); err != nil {
		return err
	}
	if err := c.Mail(s.from, nil); err != nil {
		return err
	}
	for _, to := range rcptList {
		if err := c.Rcpt(to, nil); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// handOverError turns the next hop's refusal into this side's answer: its
// own SMTP error as it is, anything else (dial, timeout) as 451.
func handOverError(hop string, err error) error {
	var smtpErr *gosmtp.SMTPError
	if errors.As(err, &smtpErr) {
		return smtpErr
	}
	log.Printf("smtp: forward to %s failed (451, sender retries): %v", hop, err)
	return &gosmtp.SMTPError{
		Code:         451,
		EnhancedCode: gosmtp.EnhancedCode{4, 4, 1},
		Message:      "next hop unavailable, try again later",
	}
}

// verdictHeader is what the next hop reads: where this side would file it.
func verdictHeader(folder string) []byte {
	if folder == "INBOX" {
		return nil
	}
	return []byte(FolderHeader + ": " + folder + "\r\n")
}

// takeVerdict reads and strips a forwarder's folder header. From anyone but
// a trusted forwarder the header is stripped and ignored.
func takeVerdict(raw []byte, trusted bool) ([]byte, string) {
	headerEnd := bytes.Index(raw, []byte("\r\n\r\n"))
	if headerEnd < 0 {
		return raw, ""
	}
	folder := ""
	var kept bytes.Buffer
	for _, line := range bytes.SplitAfter(raw[:headerEnd+2], []byte("\r\n")) {
		name, value, ok := bytes.Cut(line, []byte(":"))
		if ok && strings.EqualFold(string(bytes.TrimSpace(name)), FolderHeader) {
			if trusted {
				folder = strings.TrimSpace(string(value))
			}
			continue
		}
		kept.Write(line)
	}
	kept.Write(raw[headerEnd+2:])
	if folder != "Junk" {
		folder = ""
	}
	return kept.Bytes(), folder
}
