package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/krisamin/mail/internal/store"
	"github.com/krisamin/mail/internal/store/postgres"
)

// 셀프서비스 API (/api/me/*) — 로그인한 유저가 "본인" 계정과
// 앱 비밀번호를 관리한다. admin 그룹 불필요.
//
// 계정 매핑: OIDC sub 클레임 == 계정 신원 (0006). 계정은 첫 로그인 때
// JIT 프로비저닝(/api/me/provision)으로 생긴다 — email 도메인이 서버에
// 등록된 경우만. 주소 추가/삭제는 admin 전용.
//
// 소유권: 목록/발급은 본인 계정으로만 조회하고, revoke는 대상
// 비밀번호가 본인 소유인지 확인한 후 실행한다 (IDOR 방지).

// resolveMe는 토큰의 sub 클레임으로 본인 계정을 찾는다.
// sub 매칭 실패 시 email 소유 주소로도 시도한다 (프로비저닝 이전 조회 대비).
func (s *Server) resolveMe(w http.ResponseWriter, r *http.Request) *store.Account {
	id := IdentityFrom(r.Context())
	if id == nil || id.Subject == "" {
		writeError(w, http.StatusUnauthorized, "sub claim required")
		return nil
	}
	u, err := s.store.FindAccountBySubject(r.Context(), id.Subject)
	if err == nil {
		return u
	}
	if !errors.Is(err, store.ErrNotFound) {
		mapStoreErr(w, err)
		return nil
	}
	if id.Email != "" {
		if u, err := s.store.FindAccountByAddress(r.Context(), strings.ToLower(id.Email)); err == nil {
			return u
		}
	}
	// 계정 없음 → 아직 프로비저닝 안 된 상태
	writeError(w, http.StatusNotFound, "account not provisioned for "+id.Subject)
	return nil
}

// handleMeAccount는 본인 계정 요약.
func (s *Server) handleMeAccount(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	writeJSON(w, http.StatusOK, toAccountDTO(u))
}

// handleMeProvision은 JIT 프로비저닝 — 첫 로그인 때 RR7 콜백이 호출한다.
// 계정이 없으면 생성 (email 도메인이 등록돼 있으면 primary 주소+INBOX까지,
// 미등록이면 계정만 — 주소 없음 = 메일 사용 불가). 이미 있으면 email
// 갱신만 (멱등). 도메인 등록 여부와 무관하게 로그인은 항상 허용 —
// admin은 OIDC 그룹으로 판정되므로 빈 DB에서도 관리 화면 진입이 가능하다.
func (s *Server) handleMeProvision(w http.ResponseWriter, r *http.Request) {
	id := IdentityFrom(r.Context())
	if id == nil || id.Subject == "" {
		writeError(w, http.StatusUnauthorized, "sub claim required")
		return
	}
	if id.Email == "" {
		writeError(w, http.StatusBadRequest, "email claim required")
		return
	}
	u, err := s.store.ProvisionAccount(r.Context(), id.Subject, strings.ToLower(id.Email))
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toAccountDTO(u))
}

// handleMeAddress는 본인 소유 주소 목록 (수신/발신 가능 주소 안내용).
func (s *Server) handleMeAddress(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	addressList, err := s.store.ListAccountAddress(r.Context(), u.ID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	out := make([]addressDTO, 0, len(addressList))
	for _, a := range addressList {
		out = append(out, toAddressDTO(a))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleMeListAppPassword는 본인 앱 비밀번호 목록.
func (s *Server) handleMeListAppPassword(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	passwordList, err := s.store.ListAppPassword(r.Context(), u.ID)
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
	passwordList, err := s.store.ListAppPassword(r.Context(), u.ID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	owned := false
	for _, p := range passwordList {
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
