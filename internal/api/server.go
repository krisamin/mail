package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/krisamin/mail/internal/store"
	"github.com/krisamin/mail/internal/store/postgres"
)

// Server는 admin REST API.
type Server struct {
	store *postgres.Store // AdminStore + FindUserByID까지 필요해서 구체 타입
	auth  *Authenticator
	mux   *http.ServeMux
}

// NewServer는 라우팅을 조립한다.
func NewServer(st *postgres.Store, auth *Authenticator) *Server {
	s := &Server{store: st, auth: auth, mux: http.NewServeMux()}

	// Go 1.22+ 패턴 라우팅
	s.mux.HandleFunc("GET /api/health", s.handleHealth) // 인증 불필요

	admin := http.NewServeMux()
	admin.HandleFunc("GET /api/admin/me", s.handleMe)
	admin.HandleFunc("GET /api/admin/domains", s.handleListDomains)
	admin.HandleFunc("POST /api/admin/domains", s.handleCreateDomain)
	admin.HandleFunc("PATCH /api/admin/domains/{id}", s.handlePatchDomain)
	admin.HandleFunc("POST /api/admin/domains/{id}/dkim", s.handleGenerateDKIM)
	admin.HandleFunc("DELETE /api/admin/domains/{id}/dkim", s.handleClearDKIM)
	admin.HandleFunc("GET /api/admin/domains/{id}/users", s.handleListUsers)
	admin.HandleFunc("POST /api/admin/domains/{id}/users", s.handleCreateUser)
	admin.HandleFunc("PATCH /api/admin/users/{id}", s.handlePatchUser)
	admin.HandleFunc("GET /api/admin/users/{id}/app-passwords", s.handleListAppPasswords)
	admin.HandleFunc("POST /api/admin/users/{id}/app-passwords", s.handleCreateAppPassword)
	admin.HandleFunc("DELETE /api/admin/app-passwords/{id}", s.handleRevokeAppPassword)
	admin.HandleFunc("GET /api/admin/queue", s.handleListQueue)
	admin.HandleFunc("GET /api/admin/queue/stats", s.handleQueueStats)
	admin.HandleFunc("POST /api/admin/queue/{id}/retry", s.handleRetryQueue)

	s.mux.Handle("/api/admin/", auth.RequireAdmin(admin))

	// 셀프서비스 — 로그인한 유저 본인 계정 (그룹 불필요).
	// OIDC email 클레임 → 메일 계정 매핑. 소유권 검증 필수.
	me := http.NewServeMux()
	me.HandleFunc("GET /api/me/account", s.handleMeAccount)
	me.HandleFunc("GET /api/me/app-passwords", s.handleMeListAppPasswords)
	me.HandleFunc("POST /api/me/app-passwords", s.handleMeCreateAppPassword)
	me.HandleFunc("DELETE /api/me/app-passwords/{id}", s.handleMeRevokeAppPassword)
	s.mux.Handle("/api/me/", auth.RequireUser(me))
	return s
}

// ServeHTTP는 http.Handler 구현.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// ── 헬퍼 ────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// mapStoreErr는 store 에러 → HTTP 상태.
func mapStoreErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case strings.Contains(err.Error(), "duplicate key"):
		writeError(w, http.StatusConflict, "already exists")
	case strings.Contains(err.Error(), "잘못된"):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func pathID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

func decodeBody(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// ── 핸들러 ──────────────────────────────────────────────────

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, IdentityFrom(r.Context()))
}

// ── 도메인 ──────────────────────────────────────────────────

type domainDTO struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Active       bool   `json:"active"`
	CreatedAt    string `json:"createdAt"`
	DKIMSelector string `json:"dkimSelector"`
	// DKIMPublicTXT는 DNS에 게시할 TXT 값 (개인키는 절대 안 내려줌).
	DKIMPublicTXT string `json:"dkimPublicTxt,omitempty"`
}

func toDomainDTO(d *store.Domain) domainDTO {
	dto := domainDTO{
		ID: d.ID, Name: d.Name, Active: d.Active,
		CreatedAt:    d.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		DKIMSelector: d.DKIMSelector,
	}
	if d.DKIMPrivateKey != "" {
		if txt, err := dkimPublicTXT(d.DKIMPrivateKey); err == nil {
			dto.DKIMPublicTXT = txt
		}
	}
	return dto
}

func (s *Server) handleListDomains(w http.ResponseWriter, r *http.Request) {
	domains, err := s.store.ListDomains(r.Context())
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	out := make([]domainDTO, 0, len(domains))
	for _, d := range domains {
		out = append(out, toDomainDTO(d))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateDomain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	d, err := s.store.CreateDomain(r.Context(), req.Name)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toDomainDTO(d))
}

func (s *Server) handlePatchDomain(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		Active *bool `json:"active"`
	}
	if err := decodeBody(r, &req); err != nil || req.Active == nil {
		writeError(w, http.StatusBadRequest, "invalid body (active required)")
		return
	}
	if err := s.store.SetDomainActive(r.Context(), id, *req.Active); err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"active": *req.Active})
}

// handleGenerateDKIM은 Ed25519 키를 생성해 저장하고 DNS TXT 값을 돌려준다.
func (s *Server) handleGenerateDKIM(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		Selector string `json:"selector"`
	}
	if err := decodeBody(r, &req); err != nil || strings.TrimSpace(req.Selector) == "" {
		writeError(w, http.StatusBadRequest, "invalid body (selector required)")
		return
	}
	selector := strings.ToLower(strings.TrimSpace(req.Selector))

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "keygen failed")
		return
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "marshal failed")
		return
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	if err := s.store.SetDomainDKIM(r.Context(), id, selector, pemText); err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"selector": selector,
		"dnsName":  selector + "._domainkey",
		"dnsTxt":   "v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(pub),
	})
}

func (s *Server) handleClearDKIM(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.SetDomainDKIM(r.Context(), id, "", ""); err != nil {
		mapStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// dkimPublicTXT는 저장된 개인키에서 DNS TXT 값을 재계산한다.
func dkimPublicTXT(pemText string) (string, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return "", fmt.Errorf("pem decode")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", err
	}
	switch k := key.(type) {
	case ed25519.PrivateKey:
		pub := k.Public().(ed25519.PublicKey)
		return "v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(pub), nil
	default:
		// RSA 등 — SPKI DER로
		signer, ok := key.(interface{ Public() any })
		if !ok {
			return "", fmt.Errorf("unsupported key")
		}
		der, err := x509.MarshalPKIXPublicKey(signer.Public())
		if err != nil {
			return "", err
		}
		return "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(der), nil
	}
}

// ── 유저 ────────────────────────────────────────────────────

type userDTO struct {
	ID        int64  `json:"id"`
	DomainID  int64  `json:"domainId"`
	LocalPart string `json:"localPart"`
	Active    bool   `json:"active"`
	CreatedAt string `json:"createdAt"`
}

func toUserDTO(u *store.User) userDTO {
	return userDTO{
		ID: u.ID, DomainID: u.DomainID, LocalPart: u.LocalPart, Active: u.Active,
		CreatedAt: u.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	users, err := s.store.ListUsers(r.Context(), id)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	out := make([]userDTO, 0, len(users))
	for _, u := range users {
		out = append(out, toUserDTO(u))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		LocalPart string `json:"localPart"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	u, err := s.store.CreateUser(r.Context(), id, req.LocalPart)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toUserDTO(u))
}

func (s *Server) handlePatchUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		Active *bool `json:"active"`
	}
	if err := decodeBody(r, &req); err != nil || req.Active == nil {
		writeError(w, http.StatusBadRequest, "invalid body (active required)")
		return
	}
	if err := s.store.SetUserActive(r.Context(), id, *req.Active); err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"active": *req.Active})
}

// ── 앱 비밀번호 ─────────────────────────────────────────────

type appPasswordDTO struct {
	ID        int64   `json:"id"`
	Label     string  `json:"label"`
	Scopes    []string `json:"scopes"`
	LastUsed  *string `json:"lastUsed"`
	CreatedAt string  `json:"createdAt"`
	Revoked   bool    `json:"revoked"`
}

func toAppPasswordDTO(p *store.AppPassword) appPasswordDTO {
	dto := appPasswordDTO{
		ID: p.ID, Label: p.Label, Scopes: p.Scopes,
		CreatedAt: p.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		Revoked:   p.RevokedAt != nil,
	}
	if p.LastUsed != nil {
		s := p.LastUsed.UTC().Format("2006-01-02T15:04:05Z")
		dto.LastUsed = &s
	}
	return dto
}

func (s *Server) handleListAppPasswords(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	pws, err := s.store.ListAppPasswords(r.Context(), id)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	out := make([]appPasswordDTO, 0, len(pws))
	for _, p := range pws {
		out = append(out, toAppPasswordDTO(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateAppPassword는 랜덤 비밀번호를 생성해 해시 저장,
// 평문은 응답에 1회만 포함 (다시 조회 불가).
func (s *Server) handleCreateAppPassword(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	// 유저 존재 확인 (없는 유저에 비번 만드는 것 방지)
	if _, err := s.store.FindUserByID(r.Context(), id); err != nil {
		mapStoreErr(w, err)
		return
	}
	var req struct {
		Label string `json:"label"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	plain, err := generateAppPassword()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "generation failed")
		return
	}
	hash, err := postgres.HashPassword(plain)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash failed")
		return
	}
	p, err := s.store.CreateAppPassword(r.Context(), id, req.Label, hash)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"appPassword": toAppPasswordDTO(p),
		// 평문은 이 응답에서만 — 저장 안 함
		"plaintext": plain,
	})
}

func (s *Server) handleRevokeAppPassword(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.RevokeAppPassword(r.Context(), id); err != nil {
		mapStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// generateAppPassword는 사람이 옮겨 적기 좋은 4그룹 포맷 (예: abcd-efgh-ijkl-mnop).
func generateAppPassword() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b[0:4]) + "-" + string(b[4:8]) + "-" + string(b[8:12]) + "-" + string(b[12:16]), nil
}

// ── 발송 큐 ─────────────────────────────────────────────────

type queueDTO struct {
	ID            int64  `json:"id"`
	From          string `json:"from"`
	Rcpt          string `json:"rcpt"`
	Status        string `json:"status"`
	Attempts      int    `json:"attempts"`
	NextAttemptAt string `json:"nextAttemptAt"`
	LastError     string `json:"lastError"`
	CreatedAt     string `json:"createdAt"`
}

func (s *Server) handleListQueue(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	msgs, err := s.store.ListOutbound(r.Context(), status, 100)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	out := make([]queueDTO, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, queueDTO{
			ID: m.ID, From: m.EnvelopeFrom, Rcpt: m.EnvelopeRcpt,
			Status: m.Status, Attempts: m.Attempts,
			NextAttemptAt: m.NextAttemptAt.UTC().Format("2006-01-02T15:04:05Z"),
			LastError:     m.LastError,
			CreatedAt:     m.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleQueueStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.OutboundStats(r.Context())
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleRetryQueue(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.RetryOutbound(r.Context(), id); err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "pending"})
}
