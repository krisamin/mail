package api

import (
	"net/http"

	"github.com/krisamin/mail/internal/store"
)

// 주소 관리 API — 계정 소유 메일 주소 (admin 전용).
// 유저(계정)는 JIT 프로비저닝으로만 생기고, 주소 추가/삭제는 admin이 한다.
// localPart '*'는 catch-all (그 도메인의 모든 미지정 주소가 대상 계정에 배달).

type addressDTO struct {
	ID           int64  `json:"id"`
	DomainID     int64  `json:"domainId"`
	DomainName   string `json:"domainName"`
	LocalPart    string `json:"localPart"` // '*' = catch-all
	AccountID    int64  `json:"accountId"`
	AccountEmail string `json:"accountEmail"`
	CreatedAt    string `json:"createdAt"`
}

func toAddressDTO(a *store.Address) addressDTO {
	return addressDTO{
		ID: a.ID, DomainID: a.DomainID, DomainName: a.DomainName,
		LocalPart: a.LocalPart, AccountID: a.AccountID,
		AccountEmail: a.AccountEmail,
		CreatedAt:    a.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

// handleListDomainAddress는 도메인의 주소 목록.
func (s *Server) handleListDomainAddress(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	addressList, err := s.store.ListAddress(r.Context(), id)
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

// handleListAccountAddress는 계정의 주소 목록.
func (s *Server) handleListAccountAddress(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	addressList, err := s.store.ListAccountAddress(r.Context(), id)
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

// handleCreateAddress는 주소를 계정에 붙인다. body: {localPart, accountId}.
func (s *Server) handleCreateAddress(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		LocalPart string `json:"localPart"`
		AccountID int64  `json:"accountId"`
	}
	if err := decodeBody(r, &req); err != nil || req.AccountID == 0 {
		writeError(w, http.StatusBadRequest, "invalid body (localPart, accountId required)")
		return
	}
	// 대상 계정 존재 확인
	if _, err := s.store.FindAccountByID(r.Context(), req.AccountID); err != nil {
		mapStoreErr(w, err)
		return
	}
	a, err := s.store.CreateAddress(r.Context(), id, req.LocalPart, req.AccountID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toAddressDTO(a))
}

// handleDeleteAddress는 주소를 지운다 (마지막 일반 주소는 400).
func (s *Server) handleDeleteAddress(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteAddress(r.Context(), id); err != nil {
		mapStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
