// Package api implements the management-plane REST API (Phase 3).
//
// Authentication: OIDC Bearer token (the RR7 frontend passes it from the session).
// Authorization: the ID/access token's groups claim must include the admin group.
// The frontend checks groups too (UX), but the real defense line is here.
package api

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

// AuthConfig is the OIDC verification config.
type AuthConfig struct {
	// IssuerURL is the OIDC discovery address (e.g. http://localhost:8480/realms/mail).
	IssuerURL string
	// ClientID is for audience verification.
	ClientID string
	// AdminGroup is the group name required for admin access (e.g. mail-admin).
	AdminGroup string
	// InsecureSkipVerify is for tests — turns token verification off entirely
	// and treats every request as admin. Never in production.
	InsecureSkipVerify bool
}

// Authenticator verifies Bearer tokens and checks groups.
type Authenticator struct {
	cfg      AuthConfig
	verifier *oidc.IDTokenVerifier
}

// NewAuthenticator loads the JWKS via OIDC discovery.
func NewAuthenticator(ctx context.Context, cfg AuthConfig) (*Authenticator, error) {
	a := &Authenticator{cfg: cfg}
	if cfg.InsecureSkipVerify {
		log.Printf("api: ★ InsecureSkipVerify — token verification OFF (tests only)")
		return a, nil
	}
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, err
	}
	a.verifier = provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})
	return a, nil
}

// claims are the token claims we use.
type claims struct {
	Subject   string   `json:"sub"`
	Email     string   `json:"email"`
	GroupList []string `json:"groups"`
}

// ctxKey is the context key.
type ctxKey int

const identityKey ctxKey = iota

// Identity describes the authenticated caller.
type Identity struct {
	Subject   string
	Email     string
	GroupList []string
}

// IdentityFrom extracts the auth info from the request context.
func IdentityFrom(ctx context.Context) *Identity {
	id, _ := ctx.Value(identityKey).(*Identity)
	return id
}

// authenticate verifies the Bearer token and returns an Identity.
// In InsecureSkipVerify mode the test headers (X-Test-Sub/X-Test-Email/
// X-Test-Groups) can specify an identity (unset = admin — existing-test compat).
func (a *Authenticator) authenticate(r *http.Request) (*Identity, int, string) {
	if a.cfg.InsecureSkipVerify {
		id := &Identity{Subject: "test", Email: "test@localhost", GroupList: []string{a.cfg.AdminGroup}}
		if sub := r.Header.Get("X-Test-Sub"); sub != "" {
			id.Subject = sub
		}
		if e := r.Header.Get("X-Test-Email"); e != "" {
			id.Email = e
			// no sub given → derive from email — keeps per-user identity separation
			if r.Header.Get("X-Test-Sub") == "" {
				id.Subject = "test:" + strings.ToLower(e)
			}
		}
		if g, ok := r.Header["X-Test-Groups"]; ok {
			id.GroupList = nil
			for _, part := range strings.Split(strings.Join(g, ","), ",") {
				if part = strings.TrimSpace(part); part != "" {
					id.GroupList = append(id.GroupList, part)
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
	return &Identity{Subject: c.Subject, Email: c.Email, GroupList: c.GroupList}, 0, ""
}

// RequireUser is middleware that only verifies the Bearer token (no group).
// For self-service (/api/me/*) — any logged-in user.
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

// RequireAdmin is middleware verifying the Bearer token + the admin group.
func (a *Authenticator) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, status, msg := a.authenticate(r)
		if id == nil {
			writeError(w, status, msg)
			return
		}
		if !hasGroup(id.GroupList, a.cfg.AdminGroup) {
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
		// Keycloak may return groups as paths like "/mail-admin"
		if g == want || g == "/"+want {
			return true
		}
	}
	return false
}
