package api

import (
	"net/http"
	"strings"

	"github.com/krisamin/mail/internal/store"
	"github.com/krisamin/mail/internal/store/postgres"
)

// 셀프서비스 API (/api/me/*) — 로그인한 유저가 "본인" 메일 계정과
// 앱 비밀번호를 관리한다. admin 그룹 불필요.
//
// 계정 매핑: OIDC email 클레임 == 메일 주소 (DD-02 전제 — IdP가
// krisam.in 주소를 email로 내려준다). 매핑되는 메일 계정이 없으면
// 404 — 아직 관리자가 계정을 안 만들어준 상태.
//
// 소유권: 목록/발급은 본인 userID로만 조회하고, revoke는 대상
// 비밀번호가 본인 소유인지 확인한 후 실행한다 (IDOR 방지).

// resolveMe는 토큰의 email 클레임으로 본인 메일 계정을 찾는다.
func (s *Server) resolveMe(w http.ResponseWriter, r *http.Request) *store.User {
	id := IdentityFrom(r.Context())
	if id == nil || id.Email == "" {
		writeError(w, http.StatusUnauthorized, "email claim required")
		return nil
	}
	u, err := s.store.FindUserByAddress(r.Context(), strings.ToLower(id.Email))
	if err != nil {
		// 활성 유저 없음 → 메일 계정 미개설
		writeError(w, http.StatusNotFound, "mail account not found for "+id.Email)
		return nil
	}
	return u
}

// handleMeAccount는 본인 메일 계정 요약.
func (s *Server) handleMeAccount(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	writeJSON(w, http.StatusOK, toUserDTO(u))
}

// handleMeListAppPasswords는 본인 앱 비밀번호 목록.
func (s *Server) handleMeListAppPasswords(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	pws, err := s.store.ListAppPasswords(r.Context(), u.ID)
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

// handleMeCreateAppPassword는 본인 앱 비밀번호 발급 (평문 1회 노출).
func (s *Server) handleMeCreateAppPassword(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
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
	p, err := s.store.CreateAppPassword(r.Context(), u.ID, req.Label, hash)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"appPassword": toAppPasswordDTO(p),
		"plaintext":   plain, // 이 응답에서만 — 저장 안 함
	})
}

// handleMeRevokeAppPassword는 본인 소유 확인 후 revoke (IDOR 방지).
func (s *Server) handleMeRevokeAppPassword(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	// 소유권 검증: 본인 목록에 있는 id만 허용
	pws, err := s.store.ListAppPasswords(r.Context(), u.ID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	owned := false
	for _, p := range pws {
		if p.ID == id {
			owned = true
			break
		}
	}
	if !owned {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err := s.store.RevokeAppPassword(r.Context(), id); err != nil {
		mapStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
