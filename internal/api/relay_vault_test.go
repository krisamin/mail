package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/krisamin/mail/internal/bao/baotest"
	"github.com/krisamin/mail/internal/relaysecret"
	"github.com/krisamin/mail/internal/store/postgres"
)

// With relays backed by OpenBao the admin API keeps its shape: the password is
// write-only, responses carry hasPassword only, and the value lands in OpenBao.
func TestRelayAPIWithOpenBao(t *testing.T) {
	dsn := os.Getenv("MAIL_TEST_DSN")
	if dsn == "" {
		t.Skip("MAIL_TEST_DSN not set — skipping integration tests")
	}
	ctx := context.Background()
	st, err := postgres.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	_, _ = st.Pool().Exec(ctx, `TRUNCATE domain, account, send_counter, app_password, mailbox, message, message_flag, message_blob, outbound_queue, address, relay, setting, filter_rule RESTART IDENTITY CASCADE`)
	auth, err := NewAuthenticator(ctx, AuthConfig{AdminGroup: "mail-admin", InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	bao := baotest.New(t)
	vs := relaysecret.Wrap(st, bao.Client(t), "mail/relay")
	srv := httptest.NewServer(NewServer(st, auth).WithRelayStore(vs).WithInternal(testServiceToken))
	t.Cleanup(srv.Close)

	const secret = "re_api_secret_value"
	code, raw := icall(t, srv, "POST", "/internal/v1/admin/relay", testServiceToken, "", map[string]any{
		"name": "resend", "host": "smtp.resend.com", "username": "resend", "password": secret, "isDefault": true,
	})
	if code != 201 {
		t.Fatalf("create: %d %s", code, raw)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "mail/relay/") {
		t.Fatalf("create response leaks the password or its path: %s", raw)
	}
	var created map[string]any
	_ = json.Unmarshal(raw, &created)
	if created["hasPassword"] != true {
		t.Fatalf("hasPassword must be true after a password was set: %s", raw)
	}
	for _, k := range []string{"password", "passwordRef"} {
		if _, ok := created[k]; ok {
			t.Fatalf("response has a %q field: %s", k, raw)
		}
	}
	id, _ := created["id"].(string)
	if v, ok := bao.Value("mail/relay/" + id); !ok || v != secret {
		t.Fatalf("OpenBao has %q (found=%v) at mail/relay/%s", v, ok, id)
	}

	// list: still hasPassword, still no value
	code, raw = icall(t, srv, "GET", "/internal/v1/admin/relay", testServiceToken, "", nil)
	if code != 200 || strings.Contains(string(raw), secret) || !strings.Contains(string(raw), `"hasPassword":true`) {
		t.Fatalf("list: %d %s", code, raw)
	}

	// update without password keeps it (hasPassword stays true)
	code, raw = icall(t, srv, "PUT", "/internal/v1/admin/relay/"+id, testServiceToken, "", map[string]any{
		"name": "resend", "host": "smtp.resend.com", "username": "resend", "isDefault": true,
	})
	if code != 200 || !strings.Contains(string(raw), `"hasPassword":true`) {
		t.Fatalf("update w/o password: %d %s", code, raw)
	}
	if v, _ := bao.Value("mail/relay/" + id); v != secret {
		t.Fatalf("secret changed by a password-less update: %q", v)
	}

	// delete takes the secret with it
	if code, raw = icall(t, srv, "DELETE", "/internal/v1/admin/relay/"+id, testServiceToken, "", nil); code != 204 {
		t.Fatalf("delete: %d %s", code, raw)
	}
	if _, ok := bao.Value("mail/relay/" + id); ok {
		t.Fatal("secret survived the delete")
	}
}
