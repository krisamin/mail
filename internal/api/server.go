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

// Server는 admin REST API.
type Server struct {
	store *postgres.Store // AdminStore + FindAccountByID까지 필요해서 구체 타입
	auth  *Authenticator
	mux   *http.ServeMux
	// hostname은 MX 검증 기대값 (MAIL_HOSTNAME). 비어있으면 존재만 확인.
	hostname string
	// systemPortList는 /api/admin/system 점검 대상 (main.go에서 조립).
	systemPortList []SystemPort
	// externalHost/externalPortList는 외부 도달성 점검 대상.
	externalHost     string
	externalPortList []ExternalPort
}

// WithHostname은 DNS 검증에서 MX 기대값으로 쓸 서버 호스트네임을 지정한다.
func (s *Server) WithHostname(h string) *Server {
	s.hostname = h
	return s
}

// NewServer는 라우팅을 조립한다.
func NewServer(st *postgres.Store, auth *Authenticator) *Server {
	s := &Server{store: st, auth: auth, mux: http.NewServeMux()}

	// Go 1.22+ 패턴 라우팅
	s.mux.HandleFunc("GET /api/health", s.handleHealth) // 인증 불필요
	// 전역 표시 언어 — 읽기는 공개 (로그인 전 화면도 필요), 쓰기는 admin.
	s.mux.HandleFunc("GET /api/setting/locale", s.handleGetLocale)
	// Thunderbird autoconfig — 공개 (설정값만, 비밀 없음).
	// autoconfig.<도메인> 호스트와 .well-known 경로 둘 다 지원.
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

	// 셀프서비스 — 로그인한 유저 본인 계정 (그룹 불필요).
	// OIDC sub 클레임 → 계정 매핑 (JIT 프로비저닝). 소유권 검증 필수.
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

// validDNSLabel은 RFC 1035 DNS 라벨 검증 (a-z, 0-9, 내부 하이픈, 63자 이하).
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

// mapStoreErr는 store 에러 → HTTP 상태.
func mapStoreErr(w http.ResponseWriter, err error) {
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	// pg 에러코드로 판정 (문자열 매칭은 드라이버 메시지 변경에 깨진다)
	case errors.As(err, &pgErr) && pgErr.Code == "23505": // unique_violation
		writeError(w, http.StatusConflict, "already exists")
	case errors.As(err, &pgErr) && pgErr.Code == "23503": // foreign_key_violation
		writeError(w, http.StatusConflict, "referenced by other records")
	case strings.Contains(err.Error(), "잘못된"):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		// 내부 에러 원문(SQL/드라이버 메시지)은 클라이언트에 노출하지
		// 않는다 — 서버 로그에만 남기고 고정 문구로 응답.
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
	// 소급 프로비저닝: 이 도메인 email로 이미 로그인했던(bare) 계정들에
	// primary 주소+INBOX 생성. 실패해도 도메인 생성 자체는 유효 — 경고만.
	// 응답은 domainDTO 필드 + backfilled (flat — 기존 클라이언트 호환).
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

// handleGenerateDKIM은 DKIM 키를 생성해 저장하고 DNS TXT 값을 돌려준다.
// keyType: "rsa2048"(기본) | "ed25519".
// 기본이 RSA인 이유: Gmail 등 대형 프로바이더가 Ed25519 DKIM(RFC 8463)을
// 사실상 검증하지 못한다 — Ed25519로만 서명하면 무서명 취급될 수 있다.
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
	// DNS 라벨 검증 — 여기서 안 거르면 "<selector>._domainkey" DNS 이름이
	// 깨진 채 DB에 저장돼 서명은 되는데 검증은 안 되는 상태가 된다.
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

// ── 계정 ────────────────────────────────────────────────────

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

// handleCreateServiceAccount는 서비스 계정 생성 (admin 전용, 0007).
// 로그인 불가 — 주소+앱비밀번호만 갖는 시스템 계정. body: {email}.
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

// ── 앱 비밀번호 ─────────────────────────────────────────────

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

// handleCreateAppPassword는 랜덤 비밀번호를 생성해 해시 저장,
// 평문은 응답에 1회만 포함 (다시 조회 불가).
func (s *Server) handleCreateAppPassword(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	// 유저 존재 확인 (없는 유저에 비번 만드는 것 방지)
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
	// rejection sampling — 단순 b[i]%36은 256이 36의 배수가 아니라
	// 앞쪽 문자가 ~1.6% 더 자주 나오는 modulo bias가 생긴다.
	// 252(=36*7) 미만 바이트만 채택해 완전 균등 분포 보장.
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

// ── 발송 큐 ─────────────────────────────────────────────────

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
