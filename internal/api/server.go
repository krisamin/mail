package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/krisamin/mail/internal/store"
	"github.com/krisamin/mail/internal/store/postgres"
)

// Server is the admin REST API.
type Server struct {
	store *postgres.Store // concrete type — needs AdminStore plus FindAccountByID
	auth  *Authenticator
	mux   *http.ServeMux
	// hostname is the expected MX value (MAIL_HOSTNAME). Empty = existence check only.
	hostname string
	// systemPortList is what /api/admin/system probes (assembled in main.go).
	systemPortList []SystemPort
	// externalHost/externalPortList are the external-reachability probe targets.
	externalHost     string
	externalPortList []ExternalPort
}

// WithHostname sets the server hostname used as the expected MX in DNS checks.
func (s *Server) WithHostname(h string) *Server {
	s.hostname = h
	return s
}

// NewServer assembles the routes.
func NewServer(st *postgres.Store, auth *Authenticator) *Server {
	s := &Server{store: st, auth: auth, mux: http.NewServeMux()}

	// Go 1.22+ pattern routing
	s.mux.HandleFunc("GET /api/health", s.handleHealth) // no auth
	// global display locale — reads are public (pre-login screens need it), writes are admin.
	s.mux.HandleFunc("GET /api/setting/locale", s.handleGetLocale)
	// Thunderbird autoconfig — public (config values only, no secrets).
	// Supports both the autoconfig.<domain> host and the .well-known path.
	s.mux.HandleFunc("GET /mail/config-v1.1.xml", s.handleAutoconfigXML)
	s.mux.HandleFunc("GET /.well-known/autoconfig/mail/config-v1.1.xml", s.handleAutoconfigXML)

	admin := http.NewServeMux()
	admin.HandleFunc("GET /api/admin/me", s.handleMe)
	admin.HandleFunc("GET /api/admin/domain", s.handleListDomain)
	admin.HandleFunc("POST /api/admin/domain", s.handleCreateDomain)
	admin.HandleFunc("PATCH /api/admin/domain/{id}", s.handlePatchDomain)
	admin.HandleFunc("POST /api/admin/domain/{id}/dkim", s.handleGenerateDKIM)
	admin.HandleFunc("DELETE /api/admin/domain/{id}/dkim", s.handleClearDKIM)
	admin.HandleFunc("GET /api/admin/domain/{id}/address", s.handleListDomainAddress)
	admin.HandleFunc("POST /api/admin/domain/{id}/address", s.handleCreateAddress)
	admin.HandleFunc("DELETE /api/admin/address/{id}", s.handleDeleteAddress)
	admin.HandleFunc("GET /api/admin/account", s.handleListAccount)
	admin.HandleFunc("GET /api/admin/account/overview", s.handleAccountOverview)
	admin.HandleFunc("POST /api/admin/account/service", s.handleCreateServiceAccount)
	admin.HandleFunc("PATCH /api/admin/account/{id}", s.handlePatchAccount)
	admin.HandleFunc("GET /api/admin/account/{id}/address", s.handleListAccountAddress)
	admin.HandleFunc("POST /api/admin/account/{id}/address", s.handleCreateAccountAddress)
	admin.HandleFunc("GET /api/admin/account/{id}/app-password", s.handleListAppPassword)
	admin.HandleFunc("POST /api/admin/account/{id}/app-password", s.handleCreateAppPassword)
	admin.HandleFunc("DELETE /api/admin/app-password/{id}", s.handleRevokeAppPassword)
	admin.HandleFunc("GET /api/admin/queue", s.handleListQueue)
	admin.HandleFunc("GET /api/admin/queue/stat", s.handleQueueStat)
	admin.HandleFunc("POST /api/admin/queue/{id}/retry", s.handleRetryQueue)
	admin.HandleFunc("POST /api/admin/queue/{id}/cancel", s.handleCancelQueue)
	admin.HandleFunc("GET /api/admin/relay", s.handleListRelay)
	admin.HandleFunc("POST /api/admin/relay", s.handleCreateRelay)
	admin.HandleFunc("PUT /api/admin/relay/{id}", s.handleUpdateRelay)
	admin.HandleFunc("DELETE /api/admin/relay/{id}", s.handleDeleteRelay)
	admin.HandleFunc("PUT /api/admin/domain/{id}/relay", s.handleSetDomainRelay)
	admin.HandleFunc("GET /api/admin/domain/{id}/dns", s.handleVerifyDomainDNS)
	admin.HandleFunc("GET /api/admin/system", s.handleSystemCheck)
	admin.HandleFunc("GET /api/admin/system/external", s.handleSystemExternal)
	admin.HandleFunc("PUT /api/admin/setting/locale", s.handleSetLocale)

	s.mux.Handle("/api/admin/", auth.RequireAdmin(admin))

	// self-service — the logged-in user's own account (no group required).
	// OIDC sub claim → account mapping (JIT provisioning). Ownership checks required.
	me := http.NewServeMux()
	me.HandleFunc("GET /api/me/account", s.handleMeAccount)
	me.HandleFunc("POST /api/me/provision", s.handleMeProvision)
	me.HandleFunc("GET /api/me/address", s.handleMeAddress)
	me.HandleFunc("GET /api/me/app-password", s.handleMeListAppPassword)
	me.HandleFunc("POST /api/me/app-password", s.handleMeCreateAppPassword)
	me.HandleFunc("DELETE /api/me/app-password/{id}", s.handleMeRevokeAppPassword)
	s.mux.Handle("/api/me/", auth.RequireUser(me))
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// ── Helpers ─────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// validDNSLabel validates an RFC 1035 DNS label (a-z, 0-9, inner hyphens, ≤63 chars).
func validDNSLabel(s string) bool {
	if s == "" || len(s) > 63 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// mapStoreErr maps store errors → HTTP statuses.
func mapStoreErr(w http.ResponseWriter, err error) {
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	// judged by pg error code (string matching breaks when driver messages change)
	case errors.As(err, &pgErr) && pgErr.Code == "23505": // unique_violation
		writeError(w, http.StatusConflict, "already exists")
	case errors.As(err, &pgErr) && pgErr.Code == "23503": // foreign_key_violation
		writeError(w, http.StatusConflict, "referenced by other records")
	// validation errors from the store carry an "invalid ..." prefix → 400
	case strings.HasPrefix(err.Error(), "invalid"):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		// Raw internal errors (SQL/driver messages) are never exposed to the
		// client — server log only, fixed phrase in the response.
		log.Printf("api: internal error: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
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

// ── Handlers ────────────────────────────────────────────────

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, IdentityFrom(r.Context()))
}

// ── Domains ─────────────────────────────────────────────────

type domainDTO struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Active       bool   `json:"active"`
	CreatedAt    string `json:"createdAt"`
	DKIMSelector string `json:"dkimSelector"`
	// DKIMPublicTXT is the TXT value to publish in DNS (the private key never leaves).
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

func (s *Server) handleListDomain(w http.ResponseWriter, r *http.Request) {
	domainList, err := s.store.ListDomain(r.Context())
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	out := make([]domainDTO, 0, len(domainList))
	for _, d := range domainList {
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
	// Retroactive provisioning: create primary address + INBOX for (bare)
	// accounts that already logged in with this domain's email. A failure
	// doesn't invalidate the domain creation — warn only.
	// Response = domainDTO fields + backfilled (flat — existing-client compatible).
	backfilled, backfillErr := s.store.BackfillDomainAddress(r.Context(), d.ID)
	out := struct {
		domainDTO
		Backfilled   int    `json:"backfilled"`
		BackfillWarn string `json:"backfillWarn,omitempty"`
	}{domainDTO: toDomainDTO(d), Backfilled: backfilled}
	if backfillErr != nil {
		out.BackfillWarn = backfillErr.Error()
	}
	writeJSON(w, http.StatusCreated, out)
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

// handleGenerateDKIM generates and stores a DKIM key and returns the DNS TXT value.
// keyType: "rsa2048" (default) | "ed25519".
// RSA is the default because large providers (Gmail etc.) effectively cannot
// verify Ed25519 DKIM (RFC 8463) — signing with Ed25519 alone risks being
// treated as unsigned.
func (s *Server) handleGenerateDKIM(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		Selector string `json:"selector"`
		KeyType  string `json:"keyType"`
	}
	if err := decodeBody(r, &req); err != nil || strings.TrimSpace(req.Selector) == "" {
		writeError(w, http.StatusBadRequest, "invalid body (selector required)")
		return
	}
	selector := strings.ToLower(strings.TrimSpace(req.Selector))
	// DNS label validation — without this filter a broken "<selector>._domainkey"
	// DNS name gets stored, leaving mail signed but unverifiable.
	if !validDNSLabel(selector) {
		writeError(w, http.StatusBadRequest, "invalid selector (a-z, 0-9, hyphen; max 63 chars)")
		return
	}

	var (
		priv   any
		dnsTxt string
	)
	switch req.KeyType {
	case "", "rsa2048":
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "keygen failed")
			return
		}
		der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "marshal failed")
			return
		}
		priv = key
		dnsTxt = "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(der)
	case "ed25519":
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "keygen failed")
			return
		}
		priv = key
		dnsTxt = "v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(pub)
	default:
		writeError(w, http.StatusBadRequest, "keyType must be rsa2048 or ed25519")
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
		"dnsTxt":   dnsTxt,
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

// dkimPublicTXT recomputes the DNS TXT value from the stored private key.
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
	case *rsa.PrivateKey:
		der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
		if err != nil {
			return "", err
		}
		return "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(der), nil
	default:
		return "", fmt.Errorf("unsupported key type %T", key)
	}
}

// ── Accounts ────────────────────────────────────────────────

type accountDTO struct {
	ID        int64  `json:"id"`
	Subject   string `json:"subject"`
	Email     string `json:"email"`
	Kind      string `json:"kind"` // 'user' | 'service'
	Active    bool   `json:"active"`
	CreatedAt string `json:"createdAt"`
}

func toAccountDTO(u *store.Account) accountDTO {
	return accountDTO{
		ID: u.ID, Subject: u.OIDCSubject, Email: u.OIDCEmail, Kind: u.Kind, Active: u.Active,
		CreatedAt: u.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

// handleCreateServiceAccount creates a service account (admin only, 0007).
// No login — a system account with only addresses + app passwords. body: {email}.
func (s *Server) handleCreateServiceAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := decodeBody(r, &req); err != nil || strings.TrimSpace(req.Email) == "" {
		writeError(w, http.StatusBadRequest, "invalid body (email required)")
		return
	}
	u, err := s.store.CreateServiceAccount(r.Context(), req.Email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "domain not registered for "+req.Email)
			return
		}
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toAccountDTO(u))
}

func (s *Server) handleListAccount(w http.ResponseWriter, r *http.Request) {
	accountList, err := s.store.ListAccount(r.Context())
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	out := make([]accountDTO, 0, len(accountList))
	for _, u := range accountList {
		out = append(out, toAccountDTO(u))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePatchAccount(w http.ResponseWriter, r *http.Request) {
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
	if err := s.store.SetAccountActive(r.Context(), id, *req.Active); err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"active": *req.Active})
}

// ── App passwords ───────────────────────────────────────────

type appPasswordDTO struct {
	ID        int64    `json:"id"`
	Label     string   `json:"label"`
	ScopeList []string `json:"scopeList"`
	LastUsed  *string  `json:"lastUsed"`
	CreatedAt string   `json:"createdAt"`
	Revoked   bool     `json:"revoked"`
}

func toAppPasswordDTO(p *store.AppPassword) appPasswordDTO {
	dto := appPasswordDTO{
		ID: p.ID, Label: p.Label, ScopeList: p.ScopeList,
		CreatedAt: p.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		Revoked:   p.RevokedAt != nil,
	}
	if p.LastUsed != nil {
		s := p.LastUsed.UTC().Format("2006-01-02T15:04:05Z")
		dto.LastUsed = &s
	}
	return dto
}

func (s *Server) handleListAppPassword(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	passwordList, err := s.store.ListAppPassword(r.Context(), id)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	out := make([]appPasswordDTO, 0, len(passwordList))
	for _, p := range passwordList {
		out = append(out, toAppPasswordDTO(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateAppPassword generates a random password, stores the hash,
// and includes the plaintext exactly once in the response (never retrievable again).
func (s *Server) handleCreateAppPassword(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	// verify the user exists (no passwords for nonexistent users)
	if _, err := s.store.FindAccountByID(r.Context(), id); err != nil {
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
		// plaintext lives in this response only — never stored
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

// generateAppPassword uses a human-transcribable 4-group format (e.g. abcd-efgh-ijkl-mnop).
func generateAppPassword() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	// rejection sampling — naive b[i]%36 has modulo bias (256 isn't a multiple
	// of 36, so early alphabet chars appear ~1.6% more often).
	// Accepting only bytes < 252 (=36*7) guarantees a perfectly uniform distribution.
	const limit = byte(252)
	out := make([]byte, 0, 16)
	buf := make([]byte, 32)
	for len(out) < 16 {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, v := range buf {
			if v < limit {
				out = append(out, alphabet[int(v)%len(alphabet)])
				if len(out) == 16 {
					break
				}
			}
		}
	}
	return string(out[0:4]) + "-" + string(out[4:8]) + "-" + string(out[8:12]) + "-" + string(out[12:16]), nil
}

// ── Outbound queue ──────────────────────────────────────────

type queueDTO struct {
	ID            int64  `json:"id"`
	From          string `json:"from"`
	Rcpt          string `json:"rcpt"`
	Status        string `json:"status"`
	AttemptCount  int    `json:"attemptCount"`
	NextAttemptAt string `json:"nextAttemptAt"`
	LastError     string `json:"lastError"`
	CreatedAt     string `json:"createdAt"`
}

func (s *Server) handleListQueue(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	messageList, err := s.store.ListOutbound(r.Context(), status, 100)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	out := make([]queueDTO, 0, len(messageList))
	for _, m := range messageList {
		out = append(out, queueDTO{
			ID: m.ID, From: m.EnvelopeFrom, Rcpt: m.EnvelopeRcpt,
			Status: m.Status, AttemptCount: m.AttemptCount,
			NextAttemptAt: m.NextAttemptAt.UTC().Format("2006-01-02T15:04:05Z"),
			LastError:     m.LastError,
			CreatedAt:     m.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleQueueStat(w http.ResponseWriter, r *http.Request) {
	statMap, err := s.store.OutboundStat(r.Context())
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, statMap)
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

func (s *Server) handleCancelQueue(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.CancelOutbound(r.Context(), id); err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "canceled"})
}
