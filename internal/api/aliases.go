package api

import (
	"net/http"

	"github.com/krisamin/mail/internal/store"
)

// 별칭 관리 API — 도메인별 별칭 목록/생성/삭제 (admin 전용).
// localPart '*'는 catch-all (그 도메인의 모든 미지정 주소가 대상 유저에게 배달).

type aliasDTO struct {
	ID             int64  `json:"id"`
	DomainID       int64  `json:"domainId"`
	DomainName     string `json:"domainName"`
	LocalPart      string `json:"localPart"` // '*' = catch-all
	UserID         int64  `json:"userId"`
	UserLocalPart  string `json:"userLocalPart"`
	UserDomainName string `json:"userDomainName"`
	CreatedAt      string `json:"createdAt"`
}

func toAliasDTO(a *store.Alias) aliasDTO {
	return aliasDTO{
		ID: a.ID, DomainID: a.DomainID, DomainName: a.DomainName,
		LocalPart: a.LocalPart, UserID: a.UserID,
		UserLocalPart: a.UserLocalPart, UserDomainName: a.UserDomainName,
		CreatedAt: a.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

// handleListAliases는 도메인의 별칭 목록.
func (s *Server) handleListAliases(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	aliases, err := s.store.ListAliases(r.Context(), id)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	out := make([]aliasDTO, 0, len(aliases))
	for _, a := range aliases {
		out = append(out, toAliasDTO(a))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateAlias는 별칭 생성. body: {localPart, userId}.
func (s *Server) handleCreateAlias(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		LocalPart string `json:"localPart"`
		UserID    int64  `json:"userId"`
	}
	if err := decodeBody(r, &req); err != nil || req.UserID == 0 {
		writeError(w, http.StatusBadRequest, "invalid body (localPart, userId required)")
		return
	}
	// 대상 유저 존재 확인
	if _, err := s.store.FindUserByID(r.Context(), req.UserID); err != nil {
		mapStoreErr(w, err)
		return
	}
	a, err := s.store.CreateAlias(r.Context(), id, req.LocalPart, req.UserID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toAliasDTO(a))
}

// handleDeleteAlias는 별칭 삭제.
func (s *Server) handleDeleteAlias(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteAlias(r.Context(), id); err != nil {
		mapStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
