// Package api는 관리 플레인 REST API를 구현한다 (Phase 3).
//
// 인증: OIDC Bearer 토큰 (RR7 프론트가 세션에서 꺼내 전달).
// 인가: ID 토큰/액세스 토큰의 groups claim에 admin 그룹이 있어야 한다.
// 프론트에서도 그룹을 체크하지만(UX), 진짜 방어선은 여기다.
package api

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

// AuthConfig는 OIDC 검증 설정.
type AuthConfig struct {
	// IssuerURL은 OIDC discovery 주소 (예: http://localhost:8480/realms/mail).
	IssuerURL string
	// ClientID는 audience 검증용.
	ClientID string
	// AdminGroup은 관리자 접근에 필요한 그룹 이름 (예: mail-admin).
	AdminGroup string
	// InsecureSkipVerify는 테스트용 — 토큰 검증을 통째로 끄고
	// 모든 요청을 admin으로 취급한다. 프로덕션 금지.
	InsecureSkipVerify bool
}

// Authenticator는 Bearer 토큰을 검증하고 그룹을 확인한다.
type Authenticator struct {
	cfg      AuthConfig
	verifier *oidc.IDTokenVerifier
}

// NewAuthenticator는 OIDC discovery로 JWKS를 로드한다.
func NewAuthenticator(ctx context.Context, cfg AuthConfig) (*Authenticator, error) {
	a := &Authenticator{cfg: cfg}
	if cfg.InsecureSkipVerify {
		log.Printf("api: ★ InsecureSkipVerify — 토큰 검증 꺼짐 (테스트 전용)")
		return a, nil
	}
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, err
	}
	a.verifier = provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})
	return a, nil
}

// claims는 우리가 쓰는 토큰 클레임.
type claims struct {
	Subject string   `json:"sub"`
	Email   string   `json:"email"`
	Groups  []string `json:"groups"`
}

// ctxKey는 컨텍스트 키.
type ctxKey int

const identityKey ctxKey = iota

// Identity는 인증된 호출자 정보.
type Identity struct {
	Subject string
	Email   string
	Groups  []string
}

// IdentityFrom은 요청 컨텍스트에서 인증 정보를 꺼낸다.
func IdentityFrom(ctx context.Context) *Identity {
	id, _ := ctx.Value(identityKey).(*Identity)
	return id
}

// authenticate는 Bearer 토큰을 검증해 Identity를 돌려준다.
// InsecureSkipVerify 모드에선 테스트 헤더(X-Test-Email/X-Test-Groups)로
// 신원을 지정할 수 있다 (미지정 시 admin 취급 — 기존 테스트 호환).
func (a *Authenticator) authenticate(r *http.Request) (*Identity, int, string) {
	if a.cfg.InsecureSkipVerify {
		id := &Identity{Subject: "test", Email: "test@localhost", Groups: []string{a.cfg.AdminGroup}}
		if e := r.Header.Get("X-Test-Email"); e != "" {
			id.Email = e
		}
		if g, ok := r.Header["X-Test-Groups"]; ok {
			id.Groups = nil
			for _, part := range strings.Split(strings.Join(g, ","), ",") {
				if part = strings.TrimSpace(part); part != "" {
					id.Groups = append(id.Groups, part)
				}
			}
		}
		return id, 0, ""
	}

	raw := bearerToken(r)
	if raw == "" {
		return nil, http.StatusUnauthorized, "missing bearer token"
	}
	idToken, err := a.verifier.Verify(r.Context(), raw)
	if err != nil {
		return nil, http.StatusUnauthorized, "invalid token: " + err.Error()
	}
	var c claims
	if err := idToken.Claims(&c); err != nil {
		return nil, http.StatusUnauthorized, "invalid claims"
	}
	return &Identity{Subject: c.Subject, Email: c.Email, Groups: c.Groups}, 0, ""
}

// RequireUser는 Bearer 토큰 검증만 하는 미들웨어 (그룹 불필요).
// 셀프서비스(/api/me/*)용 — 로그인한 누구나.
func (a *Authenticator) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, status, msg := a.authenticate(r)
		if id == nil {
			writeError(w, status, msg)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey, id)))
	})
}

// RequireAdmin은 Bearer 토큰 검증 + admin 그룹 확인 미들웨어.
func (a *Authenticator) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, status, msg := a.authenticate(r)
		if id == nil {
			writeError(w, status, msg)
			return
		}
		if !hasGroup(id.Groups, a.cfg.AdminGroup) {
			writeError(w, http.StatusForbidden, "admin group required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey, id)))
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return h[7:]
	}
	return ""
}

func hasGroup(groups []string, want string) bool {
	for _, g := range groups {
		// Keycloak은 그룹을 "/mail-admin"처럼 경로로 줄 수 있음
		if g == want || g == "/"+want {
			return true
		}
	}
	return false
}
