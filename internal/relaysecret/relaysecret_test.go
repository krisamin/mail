package relaysecret_test

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"
	"github.com/google/uuid"

	"github.com/krisamin/mail/internal/bao"
	"github.com/krisamin/mail/internal/bao/baotest"
	"github.com/krisamin/mail/internal/queue"
	"github.com/krisamin/mail/internal/relaysecret"
	"github.com/krisamin/mail/internal/store"
	"github.com/krisamin/mail/internal/store/postgres"
)

const prefix = "mail/relay"

func testStore(t *testing.T) *postgres.Store {
	t.Helper()
	dsn := os.Getenv("MAIL_TEST_DSN")
	if dsn == "" {
		t.Skip("MAIL_TEST_DSN not set — skipping integration test")
	}
	st, err := postgres.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(st.Close)
	if _, err := st.Pool().Exec(context.Background(),
		`TRUNCATE domain, account, send_counter, app_password, mailbox, message, message_flag, message_blob, outbound_queue, address, relay RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return st
}

// column reads the raw password and password_ref columns of a relay row.
func column(t *testing.T, st *postgres.Store, id uuid.UUID) (pw, ref string) {
	t.Helper()
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT password, password_ref FROM relay WHERE id = $1`, id).Scan(&pw, &ref); err != nil {
		t.Fatalf("column read: %v", err)
	}
	return
}

func newRelay(name, pw string, def bool) *store.Relay {
	return &store.Relay{Name: name, Host: "smtp.example.test", Port: 587, Username: "user", Password: pw,
		StartTLS: true, IsDefault: def, Active: true}
}

func TestCreateWritesToBaoAndBlanksColumn(t *testing.T) {
	st, srv := testStore(t), baotest.New(t)
	vs := relaysecret.Wrap(st, srv.Client(t), prefix)
	ctx := context.Background()

	r, err := vs.CreateRelay(ctx, newRelay("resend", "re_topsecret", true))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	wantRef := prefix + "/" + r.ID.String()
	if v, ok := srv.Value(wantRef); !ok || v != "re_topsecret" {
		t.Fatalf("OpenBao has %q (found=%v), want the password at %s", v, ok, wantRef)
	}
	pw, ref := column(t, st, r.ID)
	if pw != "" || ref != wantRef {
		t.Fatalf("db row: password=%q ref=%q — password must be blank and ref %s", pw, ref, wantRef)
	}
	if r.Password != "" {
		t.Fatalf("returned relay carries the password")
	}
	// resolving a send reads it back from OpenBao
	got, err := vs.ResolveRelay(ctx, "any.test")
	if err != nil || got.Password != "re_topsecret" {
		t.Fatalf("resolve: %v %q", err, got.Password)
	}
}

func TestCreateWithoutPasswordTouchesNothing(t *testing.T) {
	st, srv := testStore(t), baotest.New(t)
	vs := relaysecret.Wrap(st, srv.Client(t), prefix)
	r, err := vs.CreateRelay(context.Background(), newRelay("open", "", false))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(srv.PathList()); n != 0 || srv.Logins != 0 {
		t.Fatalf("no password: OpenBao must not be touched (paths=%d logins=%d)", n, srv.Logins)
	}
	if pw, ref := column(t, st, r.ID); pw != "" || ref != "" {
		t.Fatalf("row: %q %q", pw, ref)
	}
}

func TestCreateFailsWhenBaoDown_NothingStored(t *testing.T) {
	st, srv := testStore(t), baotest.New(t)
	srv.Down = true
	vs := relaysecret.Wrap(st, srv.Client(t), prefix)
	if _, err := vs.CreateRelay(context.Background(), newRelay("resend", "re_x", true)); err == nil {
		t.Fatal("create must fail when OpenBao is down — never fall back to plaintext in the database")
	}
	list, _ := st.ListRelay(context.Background())
	if len(list) != 0 {
		t.Fatalf("a relay row was created without its secret: %d", len(list))
	}
}

func TestCreateInsertFailureRemovesSecret(t *testing.T) {
	st, srv := testStore(t), baotest.New(t)
	vs := relaysecret.Wrap(st, srv.Client(t), prefix)
	ctx := context.Background()
	if _, err := vs.CreateRelay(ctx, newRelay("dup", "pw1", false)); err != nil {
		t.Fatal(err)
	}
	// same name → UNIQUE violation after the secret was written
	if _, err := vs.CreateRelay(ctx, newRelay("dup", "pw2", false)); err == nil {
		t.Fatal("duplicate name should fail")
	}
	if n := len(srv.PathList()); n != 1 {
		t.Fatalf("orphan secret left behind: %d secrets, want 1", n)
	}
}

func TestUpdatePasswordRewritesBaoAndKeepsRefStable(t *testing.T) {
	st, srv := testStore(t), baotest.New(t)
	vs := relaysecret.Wrap(st, srv.Client(t), prefix)
	ctx := context.Background()
	r, _ := vs.CreateRelay(ctx, newRelay("resend", "old", true))
	if got, err := vs.ResolveRelay(ctx, "x.test"); err != nil || got.Password != "old" { // warms the cache
		t.Fatalf("resolve before update: %v", err)
	}

	upd := newRelay("resend", "new-pw", true)
	upd.ID = r.ID
	if _, err := vs.UpdateRelay(ctx, upd); err != nil {
		t.Fatalf("update: %v", err)
	}
	ref := prefix + "/" + r.ID.String()
	if v, _ := srv.Value(ref); v != "new-pw" {
		t.Fatalf("OpenBao value = %q", v)
	}
	if pw, gotRef := column(t, st, r.ID); pw != "" || gotRef != ref {
		t.Fatalf("row: %q %q", pw, gotRef)
	}
	// the cache must not serve the old value
	got, err := vs.ResolveRelay(ctx, "x.test")
	if err != nil || got.Password != "new-pw" {
		t.Fatalf("resolve after update: %v %q", err, got.Password)
	}
}

func TestUpdateWithEmptyPasswordKeepsSecret(t *testing.T) {
	st, srv := testStore(t), baotest.New(t)
	vs := relaysecret.Wrap(st, srv.Client(t), prefix)
	ctx := context.Background()
	r, _ := vs.CreateRelay(ctx, newRelay("resend", "keep-me", true))
	writes := srv.Writes

	upd := newRelay("resend", "", true)
	upd.ID = r.ID
	upd.Host = "smtp2.example.test"
	out, err := vs.UpdateRelay(ctx, upd)
	if err != nil {
		t.Fatal(err)
	}
	if out.Host != "smtp2.example.test" || out.PasswordRef == "" {
		t.Fatalf("update lost data: %+v", out)
	}
	if srv.Writes != writes {
		t.Fatal("empty password must not rewrite the secret")
	}
	if v, _ := srv.Value(prefix + "/" + r.ID.String()); v != "keep-me" {
		t.Fatalf("secret changed: %q", v)
	}
}

func TestUpdateMovesLegacyPlaintextRow(t *testing.T) {
	// a row created before OpenBao (password in the column) that an admin edits with a new password
	st, srv := testStore(t), baotest.New(t)
	ctx := context.Background()
	legacy, _ := st.CreateRelay(ctx, newRelay("resend", "plain-old", true))
	vs := relaysecret.Wrap(st, srv.Client(t), prefix)
	upd := newRelay("resend", "plain-new", true)
	upd.ID = legacy.ID
	if _, err := vs.UpdateRelay(ctx, upd); err != nil {
		t.Fatal(err)
	}
	if pw, ref := column(t, st, legacy.ID); pw != "" || ref == "" {
		t.Fatalf("legacy row not converted: %q %q", pw, ref)
	}
}

func TestDeleteRemovesSecret(t *testing.T) {
	st, srv := testStore(t), baotest.New(t)
	vs := relaysecret.Wrap(st, srv.Client(t), prefix)
	ctx := context.Background()
	r, _ := vs.CreateRelay(ctx, newRelay("ses", "s3s", false))
	if err := vs.DeleteRelay(ctx, r.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n := len(srv.PathList()); n != 0 {
		t.Fatalf("secret survived relay deletion: %v", srv.PathList())
	}
	if err := vs.DeleteRelay(ctx, r.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestMoveLegacy(t *testing.T) {
	st, srv := testStore(t), baotest.New(t)
	ctx := context.Background()
	a, _ := st.CreateRelay(ctx, newRelay("resend", "pw-a", true))
	b, _ := st.CreateRelay(ctx, newRelay("ses", "pw-b", false))
	c, _ := st.CreateRelay(ctx, newRelay("open", "", false))
	vs := relaysecret.Wrap(st, srv.Client(t), prefix)

	n, err := vs.MoveLegacy(ctx)
	if err != nil || n != 2 {
		t.Fatalf("move: n=%d err=%v", n, err)
	}
	for id, want := range map[uuid.UUID]string{a.ID: "pw-a", b.ID: "pw-b"} {
		ref := prefix + "/" + id.String()
		if v, _ := srv.Value(ref); v != want {
			t.Fatalf("OpenBao %s = %q", ref, v)
		}
		if pw, gotRef := column(t, st, id); pw != "" || gotRef != ref {
			t.Fatalf("row %s: %q %q", id, pw, gotRef)
		}
	}
	if pw, ref := column(t, st, c.ID); pw != "" || ref != "" {
		t.Fatalf("passwordless relay touched: %q %q", pw, ref)
	}
	// idempotent: a second run moves nothing and writes nothing
	writes := srv.Writes
	n, err = vs.MoveLegacy(ctx)
	if err != nil || n != 0 || srv.Writes != writes {
		t.Fatalf("second run: n=%d err=%v writes %d→%d", n, err, writes, srv.Writes)
	}
}

func TestMoveLegacyFailureKeepsPlaintextAndSending(t *testing.T) {
	st, srv := testStore(t), baotest.New(t)
	ctx := context.Background()
	a, _ := st.CreateRelay(ctx, newRelay("resend", "pw-a", true))
	srv.Down = true
	vs := relaysecret.Wrap(st, srv.Client(t), prefix)
	n, err := vs.MoveLegacy(ctx)
	if err == nil || n != 0 {
		t.Fatalf("want error and 0 moved, got n=%d err=%v", n, err)
	}
	if strings.Contains(err.Error(), "pw-a") {
		t.Fatal("error message leaks the password")
	}
	if pw, ref := column(t, st, a.ID); pw != "pw-a" || ref != "" {
		t.Fatalf("a failed move must leave the row alone: %q %q", pw, ref)
	}
	// the row still resolves with its database password
	got, err := vs.ResolveRelay(ctx, "x.test")
	if err != nil || got.Password != "pw-a" {
		t.Fatalf("resolve legacy row: %v %q", err, got.Password)
	}
	// OpenBao back → next run moves it
	srv.Down = false
	if n, err := vs.MoveLegacy(ctx); err != nil || n != 1 {
		t.Fatalf("retry: n=%d err=%v", n, err)
	}
}

func TestResolveCacheAndStaleFallback(t *testing.T) {
	st, srv := testStore(t), baotest.New(t)
	ctx := context.Background()
	vs := relaysecret.Wrap(st, srv.Client(t), prefix)
	r, _ := vs.CreateRelay(ctx, newRelay("resend", "pw", true))

	for i := 0; i < 5; i++ {
		if got, err := vs.ResolveRelay(ctx, "x.test"); err != nil || got.Password != "pw" {
			t.Fatalf("resolve %d: %v", i, err)
		}
	}
	if srv.Reads != 1 {
		t.Fatalf("5 sends should cost 1 OpenBao read, got %d", srv.Reads)
	}

	// cache expired + OpenBao down → the old value still sends (stale window)
	vs.SetClockForTest(time.Now().Add(2 * time.Minute))
	srv.Down = true
	if got, err := vs.ResolveRelay(ctx, "x.test"); err != nil || got.Password != "pw" {
		t.Fatalf("stale fallback: %v %q", err, got.Password)
	}
	// past the stale window → error (transient: the worker retries)
	vs.SetClockForTest(time.Now().Add(30 * time.Minute))
	if _, err := vs.ResolveRelay(ctx, "x.test"); err == nil {
		t.Fatal("beyond the stale window an unreachable OpenBao must be an error")
	}

	// secret deleted in OpenBao behind our back → error, no stale reuse
	srv.Down = false
	vs.SetClockForTest(time.Now().Add(2 * time.Minute))
	if err := srv.Client(t).Delete(ctx, prefix+"/"+r.ID.String()); err != nil {
		t.Fatal(err)
	}
	_, err := vs.ResolveRelay(ctx, "x.test")
	if err == nil || !errors.Is(err, bao.ErrNotFound) {
		t.Fatalf("missing secret must surface ErrNotFound, got %v", err)
	}
}

// ── end to end: ResolvingSender → real SMTP relay that checks AUTH ──────────

type authBackend struct {
	wantUser, wantPass string
	mu                 sync.Mutex
	authed             int
	body               chan string
}

func (b *authBackend) NewSession(_ *gosmtp.Conn) (gosmtp.Session, error) {
	return &authSession{b: b}, nil
}

type authSession struct {
	b  *authBackend
	ok bool
}

func (s *authSession) AuthMechanisms() []string { return []string{sasl.Plain} }
func (s *authSession) Auth(string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, user, pass string) error {
		if user != s.b.wantUser || pass != s.b.wantPass {
			return errors.New("bad credentials")
		}
		s.ok = true
		s.b.mu.Lock()
		s.b.authed++
		s.b.mu.Unlock()
		return nil
	}), nil
}
func (s *authSession) Mail(string, *gosmtp.MailOptions) error {
	if !s.ok {
		return &gosmtp.SMTPError{Code: 530, EnhancedCode: gosmtp.EnhancedCode{5, 7, 0}, Message: "auth required"}
	}
	return nil
}
func (s *authSession) Rcpt(string, *gosmtp.RcptOptions) error { return nil }
func (s *authSession) Data(r io.Reader) error {
	b, _ := io.ReadAll(r)
	s.b.body <- string(b)
	return nil
}
func (s *authSession) Reset()        {}
func (s *authSession) Logout() error { return nil }

func TestSendAuthenticatesWithPasswordFromBao(t *testing.T) {
	st, srv := testStore(t), baotest.New(t)
	ctx := context.Background()

	backend := &authBackend{wantUser: "resend", wantPass: "re_from_openbao", body: make(chan string, 1)}
	smtpSrv := gosmtp.NewServer(backend)
	smtpSrv.Domain = "relay.test"
	smtpSrv.AllowInsecureAuth = true
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = smtpSrv.Serve(ln) }()
	t.Cleanup(func() { _ = smtpSrv.Close() })
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)

	vs := relaysecret.Wrap(st, srv.Client(t), prefix)
	if _, err := vs.CreateRelay(ctx, &store.Relay{Name: "resend", Host: host, Port: port,
		Username: "resend", Password: "re_from_openbao", StartTLS: false, IsDefault: true, Active: true}); err != nil {
		t.Fatal(err)
	}

	sender := queue.NewResolvingSender(vs)
	if err := sender.Send(ctx, "me@krisam.in", "you@external.test", []byte("Subject: hi\r\n\r\nbody\r\n")); err != nil {
		t.Fatalf("send through the relay: %v", err)
	}
	select {
	case got := <-backend.body:
		if !strings.Contains(got, "Subject: hi") {
			t.Fatalf("relay got: %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("relay received nothing")
	}
	if backend.authed != 1 {
		t.Fatalf("relay authenticated %d times, want 1", backend.authed)
	}
	// and the database never held the password
	var pw string
	_ = st.Pool().QueryRow(ctx, `SELECT password FROM relay WHERE name = 'resend'`).Scan(&pw)
	if pw != "" {
		t.Fatalf("password column not blank: %q", pw)
	}
}

func TestSendRefusesRowWithRefButNoBaoWiring(t *testing.T) {
	// MAIL_BAO_* unset on a database whose rows already point at OpenBao: a clear transient
	// error, not an AUTH attempt with an empty password.
	st, srv := testStore(t), baotest.New(t)
	ctx := context.Background()
	vs := relaysecret.Wrap(st, srv.Client(t), prefix)
	if _, err := vs.CreateRelay(ctx, newRelay("resend", "pw", true)); err != nil {
		t.Fatal(err)
	}
	err := queue.NewResolvingSender(st).Send(ctx, "me@krisam.in", "you@external.test", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "OpenBao") {
		t.Fatalf("want an OpenBao wiring error, got %v", err)
	}
	var perm *queue.PermanentError
	if errors.As(err, &perm) {
		t.Fatal("must be transient (retry), not a bounce")
	}
}

// flakyVault wraps a real fake with per-path failure and a lying read.
type flakyVault struct {
	relaysecret.Vault
	failPut  map[string]bool
	lieOnGet bool
}

func (f *flakyVault) Put(ctx context.Context, path, v string) error {
	if f.failPut[path] {
		return errors.New("simulated put failure")
	}
	return f.Vault.Put(ctx, path, v)
}

func (f *flakyVault) Get(ctx context.Context, path string) (string, error) {
	v, err := f.Vault.Get(ctx, path)
	if f.lieOnGet && err == nil {
		return v + "-different", nil
	}
	return v, err
}

func TestMoveLegacyRefusesWhenReadBackDiffers(t *testing.T) {
	st, srv := testStore(t), baotest.New(t)
	ctx := context.Background()
	a, _ := st.CreateRelay(ctx, newRelay("resend", "pw-a", true))
	fv := &flakyVault{Vault: srv.Client(t), lieOnGet: true}
	n, err := relaysecret.Wrap(st, fv, prefix).MoveLegacy(ctx)
	if err == nil || n != 0 {
		t.Fatalf("a read-back mismatch must not count as moved: n=%d err=%v", n, err)
	}
	if pw, ref := column(t, st, a.ID); pw != "pw-a" || ref != "" {
		t.Fatalf("row must keep its plaintext when the copy cannot be verified: %q %q", pw, ref)
	}
}

func TestMoveLegacyOneFailureDoesNotStopTheOthers(t *testing.T) {
	st, srv := testStore(t), baotest.New(t)
	ctx := context.Background()
	a, _ := st.CreateRelay(ctx, newRelay("a-first", "pw-a", true))
	b, _ := st.CreateRelay(ctx, newRelay("b-second", "pw-b", false))
	// ListRelay orders by name, so a-first is tried first and fails
	fv := &flakyVault{Vault: srv.Client(t), failPut: map[string]bool{prefix + "/" + a.ID.String(): true}}
	n, err := relaysecret.Wrap(st, fv, prefix).MoveLegacy(ctx)
	if err == nil || n != 1 || !strings.Contains(err.Error(), "a-first") {
		t.Fatalf("want 1 moved and an error naming a-first, got n=%d err=%v", n, err)
	}
	if pw, ref := column(t, st, b.ID); pw != "" || ref == "" {
		t.Fatalf("b-second should have moved: %q %q", pw, ref)
	}
	if pw, _ := column(t, st, a.ID); pw != "pw-a" {
		t.Fatalf("a-first must keep its plaintext: %q", pw)
	}
}
