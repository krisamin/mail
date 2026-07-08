package api

import (
	"net/http"

	"github.com/krisamin/mail/internal/store"
)

// relay 관리 (0005) — 발송 relay CRUD + 도메인별 지정.
// ★password는 쓰기 전용: 응답에 절대 포함하지 않는다 (hasPassword로만 노출).

type relayDTO struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Username  string `json:"username"`
	StartTLS  bool   `json:"starttls"`
	IsDefault bool   `json:"isDefault"`
	Active    bool   `json:"active"`
	CreatedAt string `json:"createdAt"`
	// HasPassword는 password 설정 여부만 알려준다 (값은 절대 안 내려줌).
	HasPassword bool `json:"hasPassword"`
}

func toRelayDTO(r *store.Relay) relayDTO {
	return relayDTO{
		ID: r.ID, Name: r.Name, Host: r.Host, Port: r.Port,
		Username: r.Username, StartTLS: r.StartTLS,
		IsDefault: r.IsDefault, Active: r.Active,
		CreatedAt:   r.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		HasPassword: r.Password != "",
	}
}

type relayReq struct {
	Name      string `json:"name"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Username  string `json:"username"`
	Password  string `json:"password"` // 빈 문자열 = (수정 시) 기존 유지
	StartTLS  *bool  `json:"starttls"`
	IsDefault bool   `json:"isDefault"`
	Active    *bool  `json:"active"`
}

func (req *relayReq) toRelay() *store.Relay {
	r := &store.Relay{
		Name: req.Name, Host: req.Host, Port: req.Port,
		Username: req.Username, Password: req.Password,
		IsDefault: req.IsDefault,
		StartTLS:  true, Active: true,
	}
	if req.Port == 0 {
		r.Port = 587
	}
	if req.StartTLS != nil {
		r.StartTLS = *req.StartTLS
	}
	if req.Active != nil {
		r.Active = *req.Active
	}
	return r
}

func (s *Server) handleListRelay(w http.ResponseWriter, r *http.Request) {
	relayList, err := s.store.ListRelay(r.Context())
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	out := make([]relayDTO, 0, len(relayList))
	for _, rl := range relayList {
		out = append(out, toRelayDTO(rl))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateRelay(w http.ResponseWriter, r *http.Request) {
	var req relayReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	created, err := s.store.CreateRelay(r.Context(), req.toRelay())
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toRelayDTO(created))
}

func (s *Server) handleUpdateRelay(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req relayReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	rl := req.toRelay()
	rl.ID = id
	updated, err := s.store.UpdateRelay(r.Context(), rl)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toRelayDTO(updated))
}

func (s *Server) handleDeleteRelay(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteRelay(r.Context(), id); err != nil {
		mapStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSetDomainRelay는 도메인 발신 relay 지정. relayId null = default 사용.
func (s *Server) handleSetDomainRelay(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		RelayID *int64 `json:"relayId"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.store.SetDomainRelay(r.Context(), id, req.RelayID); err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"relayId": req.RelayID})
}
