package smtp

import (
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	gosmtp "github.com/emersion/go-smtp"
)

// Domain forwarding, without a database: the forwarded path never touches
// the store (no local lookup, no permission, greylist off), so a nil store
// proves that too.

type hopMail struct {
	from     string
	rcptList []string
	body     string
}

type hopBackend struct {
	mu     sync.Mutex
	got    []hopMail
	refuse string // a recipient the next hop does not know
}

func (b *hopBackend) NewSession(*gosmtp.Conn) (gosmtp.Session, error) {
	return &hopSession{b: b}, nil
}

type hopSession struct {
	b    *hopBackend
	mail hopMail
}

func (s *hopSession) AuthPlain(string, string) error { return nil }
func (s *hopSession) Mail(from string, _ *gosmtp.MailOptions) error {
	s.mail.from = from
	return nil
}
func (s *hopSession) Rcpt(to string, _ *gosmtp.RcptOptions) error {
	if to == s.b.refuse {
		return &gosmtp.SMTPError{Code: 550, EnhancedCode: gosmtp.EnhancedCode{5, 1, 1}, Message: "no such user"}
	}
	s.mail.rcptList = append(s.mail.rcptList, to)
	return nil
}
func (s *hopSession) Data(r io.Reader) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mail.body = string(raw)
	s.b.mu.Lock()
	s.b.got = append(s.b.got, s.mail)
	s.b.mu.Unlock()
	return nil
}
func (s *hopSession) Reset()        {}
func (s *hopSession) Logout() error { return nil }

func listen(t *testing.T, backend gosmtp.Backend) string {
	t.Helper()
	srv := gosmtp.NewServer(backend)
	srv.Domain = "test.local"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

const forwardBody = "From: a@example.com\r\nTo: ann@ara.test\r\nSubject: hi\r\n\r\nhello\r\n"

func mxWithForward(t *testing.T, hop string) string {
	t.Helper()
	forward, err := ParseForwardMap("ara.test=" + hop)
	if err != nil {
		t.Fatal(err)
	}
	return listen(t, NewBackend(nil, "mx.test").WithForward(forward))
}

func TestForwardHandsOverWholeMessage(t *testing.T) {
	hop := &hopBackend{}
	mx := mxWithForward(t, listen(t, hop))
	if err := sendSMTP(t, mx, "a@example.com", []string{"ann@ara.test", "bob@ARA.test"}, forwardBody); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(hop.got) != 1 {
		t.Fatalf("next hop got %d transactions, want 1", len(hop.got))
	}
	got := hop.got[0]
	if got.from != "a@example.com" || strings.Join(got.rcptList, ",") != "ann@ara.test,bob@ARA.test" {
		t.Fatalf("envelope = %+v", got)
	}
	if !strings.HasPrefix(got.body, "Received: from sender.example.com") || !strings.HasSuffix(got.body, forwardBody) {
		t.Fatalf("body not handed over whole with a Received line:\n%s", got.body)
	}
	if strings.Contains(got.body, FolderHeader) {
		t.Fatal("an INBOX verdict adds no folder header")
	}
	t.Log("✔ forwarded domain: one transaction, same envelope, body whole behind Received")
}

func TestForwardRefusalReachesSender(t *testing.T) {
	hop := &hopBackend{refuse: "nobody@ara.test"}
	mx := mxWithForward(t, listen(t, hop))
	err := sendSMTP(t, mx, "a@example.com", []string{"nobody@ara.test"}, forwardBody)
	var smtpErr *gosmtp.SMTPError
	if !errors.As(err, &smtpErr) || smtpErr.Code != 550 {
		t.Fatalf("want the next hop's 550, got %v", err)
	}
	if len(hop.got) != 0 {
		t.Fatal("a refused recipient must not be delivered")
	}
	t.Log("✔ next hop's 550 is the sender's 550 (no accept-then-bounce)")
}

func TestForwardHopDownIsTemporary(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	ln.Close()
	mx := mxWithForward(t, dead)
	err = sendSMTP(t, mx, "a@example.com", []string{"ann@ara.test"}, forwardBody)
	var smtpErr *gosmtp.SMTPError
	if !errors.As(err, &smtpErr) || smtpErr.Code != 451 {
		t.Fatalf("want 451 so the sender retries, got %v", err)
	}
	t.Log("✔ next hop down: 451, sender keeps the mail and retries")
}

func TestVerdictHeaderTrustedOnly(t *testing.T) {
	raw := []byte("X-Maild-Folder: Junk\r\nSubject: s\r\n\r\nbody\r\n")
	kept, folder := takeVerdict(raw, false)
	if folder != "" || strings.Contains(string(kept), FolderHeader) {
		t.Fatalf("untrusted: header must be stripped and ignored, got folder=%q\n%s", folder, kept)
	}
	kept, folder = takeVerdict(raw, true)
	if folder != "Junk" || strings.Contains(string(kept), FolderHeader) || !strings.HasSuffix(string(kept), "Subject: s\r\n\r\nbody\r\n") {
		t.Fatalf("trusted: want Junk and the header gone, got %q\n%s", folder, kept)
	}
	if _, f := takeVerdict([]byte("X-Maild-Folder: Trash\r\n\r\nb"), true); f != "" {
		t.Fatal("only Junk is a verdict")
	}
	if _, err := ParseForwardMap("ara.test"); err == nil {
		t.Fatal("an entry without a hop must be refused")
	}
	t.Log("✔ folder verdict read only from a trusted forwarder, stripped always")
}
