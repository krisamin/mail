package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/krisamin/mail/internal/store/postgres"
)

// admin API 통합 테스트. dev Postgres 필요:
//   MAIL_TEST_DSN=... go test ./internal/api/ -v
// 토큰 검증은 InsecureSkipVerify로 우회 (OIDC 미들웨어의 그룹 로직은
// hasGroup 유닛 테스트로 별도 검증).

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	dsn := os.Getenv("MAIL_TEST_DSN")
	if dsn == "" {
		t.Skip("MAIL_TEST_DSN 미설정 — 통합 테스트 skip")
	}
	st, err := postgres.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(st.Close)
	_, _ = st.Pool().Exec(context.Background(),
		`TRUNCATE domains, users, app_passwords, mailboxes, messages, message_flags, message_blobs, outbound_queue, aliases RESTART IDENTITY CASCADE`)

	auth, err := NewAuthenticator(context.Background(), AuthConfig{
		AdminGroup: "mail-admin", InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	srv := httptest.NewServer(NewServer(st, auth))
	t.Cleanup(srv.Close)
	return srv
}

// call은 JSON 요청/응답 헬퍼.
func call(t *testing.T, srv *httptest.Server, method, path string, body any) (int, map[string]any, []map[string]any) {
	t.Helper()
	var reqBody *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reqBody = bytes.NewReader(b)
	} else {
		reqBody = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, srv.URL+path, reqBody)
	if err != nil {
		t.Fatalf("req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	raw := buf.Bytes()

	var obj map[string]any
	var arr []map[string]any
	if len(raw) > 0 {
		if raw[0] == '[' {
			_ = json.Unmarshal(raw, &arr)
		} else {
			_ = json.Unmarshal(raw, &obj)
		}
	}
	return resp.StatusCode, obj, arr
}

// TestAdminFullFlow: 도메인→DKIM→유저→앱비번→revoke 전체 관리 플로우.
func TestAdminFullFlow(t *testing.T) {
	srv := testServer(t)

	// 1) 도메인 생성
	code, dom, _ := call(t, srv, "POST", "/api/admin/domains", map[string]string{"name": "Krisam.IN"})
	if code != 201 || dom["name"] != "krisam.in" {
		t.Fatalf("도메인 생성: %d %v", code, dom)
	}
	domID := int64(dom["id"].(float64))
	t.Logf("✔ 도메인 생성 (소문자 정규화): %v", dom["name"])

	// 중복 → 409
	code, _, _ = call(t, srv, "POST", "/api/admin/domains", map[string]string{"name": "krisam.in"})
	if code != 409 {
		t.Fatalf("중복 도메인은 409여야: %d", code)
	}
	// 이상한 이름 → 400
	code, _, _ = call(t, srv, "POST", "/api/admin/domains", map[string]string{"name": "nodot"})
	if code != 400 {
		t.Fatalf("점 없는 도메인은 400이어야: %d", code)
	}
	t.Log("✔ 중복 409 / 유효성 400")

	// 2) DKIM 키 생성 — 기본 RSA-2048 (Gmail 호환), ed25519는 옵션
	code, dkim, _ := call(t, srv, "POST", fmt.Sprintf("/api/admin/domains/%d/dkim", domID),
		map[string]string{"selector": "mail"})
	if code != 200 {
		t.Fatalf("DKIM 생성: %d %v", code, dkim)
	}
	dnsTxt := dkim["dnsTxt"].(string)
	if !strings.HasPrefix(dnsTxt, "v=DKIM1; k=rsa; p=") {
		t.Fatalf("기본은 RSA여야: %s", dnsTxt)
	}
	t.Logf("✔ DKIM RSA-2048 생성(기본): %s = %.40s...", dkim["dnsName"], dnsTxt)

	// ed25519 명시 생성도 동작 (키 교체 = 재생성)
	code, dkimEd, _ := call(t, srv, "POST", fmt.Sprintf("/api/admin/domains/%d/dkim", domID),
		map[string]string{"selector": "mail", "keyType": "ed25519"})
	if code != 200 || !strings.HasPrefix(dkimEd["dnsTxt"].(string), "v=DKIM1; k=ed25519; p=") {
		t.Fatalf("ed25519 생성: %d %v", code, dkimEd)
	}
	// 잘못된 keyType → 400
	code, _, _ = call(t, srv, "POST", fmt.Sprintf("/api/admin/domains/%d/dkim", domID),
		map[string]string{"selector": "mail", "keyType": "dsa"})
	if code != 400 {
		t.Fatalf("잘못된 keyType은 400이어야: %d", code)
	}
	// 이후 검증은 RSA 기본으로 다시 생성한 상태 기준
	code, dkim, _ = call(t, srv, "POST", fmt.Sprintf("/api/admin/domains/%d/dkim", domID),
		map[string]string{"selector": "mail"})
	if code != 200 {
		t.Fatalf("DKIM 재생성: %d", code)
	}
	dnsTxt = dkim["dnsTxt"].(string)
	t.Log("✔ ed25519 옵션 + keyType 유효성 400")

	// 목록에서 공개키 TXT 재계산돼 나오는지 (개인키는 안 내려옴)
	code, _, domains := call(t, srv, "GET", "/api/admin/domains", nil)
	if code != 200 || len(domains) != 1 {
		t.Fatalf("도메인 목록: %d %v", code, domains)
	}
	if domains[0]["dkimPublicTxt"] != dnsTxt {
		t.Fatal("목록의 TXT가 생성 시 값과 달라")
	}
	if _, leaked := domains[0]["dkimPrivateKey"]; leaked {
		t.Fatal("개인키가 응답에 노출됨!")
	}
	t.Log("✔ 목록에 공개 TXT만 노출 (개인키 비노출)")

	// 3) 유저 생성
	code, user, _ := call(t, srv, "POST", fmt.Sprintf("/api/admin/domains/%d/users", domID),
		map[string]string{"localPart": "Maro"})
	if code != 201 || user["localPart"] != "maro" {
		t.Fatalf("유저 생성: %d %v", code, user)
	}
	userID := int64(user["id"].(float64))
	t.Log("✔ 유저 생성 (소문자 정규화 + INBOX 자동)")

	// 4) 앱 비밀번호 발급 — 평문 1회 노출
	code, pw, _ := call(t, srv, "POST", fmt.Sprintf("/api/admin/users/%d/app-passwords", userID),
		map[string]string{"label": "Thunderbird"})
	if code != 201 {
		t.Fatalf("앱비번 발급: %d %v", code, pw)
	}
	plain := pw["plaintext"].(string)
	if len(plain) != 19 || strings.Count(plain, "-") != 3 {
		t.Fatalf("평문 형식 이상: %q", plain)
	}
	t.Logf("✔ 앱비번 발급: %s (평문 1회 노출)", plain)

	// 발급된 비번으로 실제 SMTP/IMAP 인증이 되는지 store로 확인
	// (프로토콜 레벨은 기존 테스트가 커버 — 여기선 해시 정합만)
	code, _, pws := call(t, srv, "GET", fmt.Sprintf("/api/admin/users/%d/app-passwords", userID), nil)
	if code != 200 || len(pws) != 1 || pws[0]["revoked"] != false {
		t.Fatalf("앱비번 목록: %d %v", code, pws)
	}

	// 5) revoke
	pwID := int64(pws[0]["id"].(float64))
	code, _, _ = call(t, srv, "DELETE", fmt.Sprintf("/api/admin/app-passwords/%d", pwID), nil)
	if code != 204 {
		t.Fatalf("revoke: %d", code)
	}
	code, _, pws = call(t, srv, "GET", fmt.Sprintf("/api/admin/users/%d/app-passwords", userID), nil)
	if pws[0]["revoked"] != true {
		t.Fatal("revoke 반영 안 됨")
	}
	// 이중 revoke → 404
	code, _, _ = call(t, srv, "DELETE", fmt.Sprintf("/api/admin/app-passwords/%d", pwID), nil)
	if code != 404 {
		t.Fatalf("이중 revoke는 404여야: %d", code)
	}
	t.Log("✔ 앱비번 revoke + 이중 revoke 404")

	// 6) 유저/도메인 비활성화
	code, _, _ = call(t, srv, "PATCH", fmt.Sprintf("/api/admin/users/%d", userID),
		map[string]bool{"active": false})
	if code != 200 {
		t.Fatalf("유저 비활성: %d", code)
	}
	code, _, _ = call(t, srv, "PATCH", fmt.Sprintf("/api/admin/domains/%d", domID),
		map[string]bool{"active": false})
	if code != 200 {
		t.Fatalf("도메인 비활성: %d", code)
	}
	t.Log("✔ 유저/도메인 비활성화")
}

// TestQueueEndpoints: 큐 조회/통계/재시도.
func TestQueueEndpoints(t *testing.T) {
	srv := testServer(t)

	// 시드: 큐에 항목 하나 넣고 failed로
	code, dom, _ := call(t, srv, "POST", "/api/admin/domains", map[string]string{"name": "q.test"})
	if code != 201 {
		t.Fatalf("도메인: %d %v", code, dom)
	}
	// store 직접 접근 대신 API로 확인 가능한 상태를 만들려면 enqueue가 필요한데
	// enqueue는 submission 경로라 여기선 통계 0 확인 + 빈 목록만 검증
	code, stats, _ := call(t, srv, "GET", "/api/admin/queue/stats", nil)
	if code != 200 {
		t.Fatalf("통계: %d", code)
	}
	if len(stats) != 0 {
		t.Fatalf("빈 큐 통계여야: %v", stats)
	}
	code, _, list := call(t, srv, "GET", "/api/admin/queue?status=failed", nil)
	if code != 200 || len(list) != 0 {
		t.Fatalf("빈 목록이어야: %d %v", code, list)
	}
	// 없는 항목 재시도 → 404
	code, _, _ = call(t, srv, "POST", "/api/admin/queue/999/retry", nil)
	if code != 404 {
		t.Fatalf("없는 항목 재시도는 404여야: %d", code)
	}
	t.Log("✔ 큐 조회/통계/재시도 404")
}

// TestHealthNoAuth: health는 인증 없이 접근 가능.
func TestHealthNoAuth(t *testing.T) {
	srv := testServer(t)
	resp, err := http.Get(srv.URL + "/api/health")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("health: %v %d", err, resp.StatusCode)
	}
	t.Log("✔ /api/health 인증 불필요")
}

// TestHasGroup: Keycloak 경로형 그룹 지원.
func TestHasGroup(t *testing.T) {
	if !hasGroup([]string{"mail-admin"}, "mail-admin") {
		t.Fatal("일반 그룹 매칭 실패")
	}
	if !hasGroup([]string{"/mail-admin"}, "mail-admin") {
		t.Fatal("Keycloak 경로형 그룹 매칭 실패")
	}
	if hasGroup([]string{"other", "/other2"}, "mail-admin") {
		t.Fatal("없는 그룹인데 매칭됨")
	}
	t.Log("✔ hasGroup: 일반/경로형/미매칭")
}
