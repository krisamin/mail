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

// RequireAdmin은 Bearer 토큰 검증 + admin 그룹 확인 미들웨어.
func (a *Authenticator) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.InsecureSkipVerify {
			ctx := context.WithValue(r.Context(), identityKey,
				&Identity{Subject: "test", Email: "test@localhost", Groups: []string{a.cfg.AdminGroup}})
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		raw := bearerToken(r)
		if raw == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		idToken, err := a.verifier.Verify(r.Context(), raw)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid token: "+err.Error())
			return
		}
		var c claims
		if err := idToken.Claims(&c); err != nil {
			writeError(w, http.StatusUnauthorized, "invalid claims")
			return
		}
		if !hasGroup(c.Groups, a.cfg.AdminGroup) {
			writeError(w, http.StatusForbidden, "admin group required")
			return
		}

		ctx := context.WithValue(r.Context(), identityKey,
			&Identity{Subject: c.Subject, Email: c.Email, Groups: c.Groups})
		next.ServeHTTP(w, r.WithContext(ctx))
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
