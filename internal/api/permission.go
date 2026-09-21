package api

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/krisamin/mail/internal/store"
)

// Permission endpoints (0004).
//
// Two locks: the account switch and the domain switch. The admin edits both
// here; every enforcement point reads them through internal/policy.

// PUT /api/admin/account/{id}/permission
func (s *Server) handleSetAccountPermission(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		CanSend            bool `json:"canSend"`
		CanSendExternal    bool `json:"canSendExternal"`
		CanReceiveExternal bool `json:"canReceiveExternal"`
		// DailySendLimit is recipients per UTC day; null or 0 = unlimited.
		DailySendLimit *int `json:"dailySendLimit"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	limit := req.DailySendLimit
	if limit != nil && *limit <= 0 {
		limit = nil
	}
	u, err := s.store.SetAccountPermission(r.Context(), id, store.AccountPermission{
		CanSend:            req.CanSend,
		CanSendExternal:    req.CanSendExternal,
		CanReceiveExternal: req.CanReceiveExternal,
		DailySendLimit:     limit,
	})
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toAccountDTO(u))
}

// PUT /api/admin/domain/{id}/permission
func (s *Server) handleSetDomainPermission(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		AllowSendExternal    bool `json:"allowSendExternal"`
		AllowReceiveExternal bool `json:"allowReceiveExternal"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.store.SetDomainPermission(r.Context(), id, req.AllowSendExternal, req.AllowReceiveExternal); err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// GET /api/me/permission — what the signed-in person is allowed to do, and
// how much of today's allowance is left. Read-only: a person cannot widen
// their own permissions.
func (s *Server) handleMePermission(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	dto := toAccountDTO(u)
	if used, err := s.store.SendCountToday(r.Context(), u.ID); err == nil {
		dto.SentToday = used
	}
	writeJSON(w, http.StatusOK, dto)
}

// normalizeScopeList keeps only scopes we know. An empty result means full
// access (both protocols) — the same meaning a password issued before scopes
// existed carries, so the UI never has to send "both" explicitly.
func normalizeScopeList(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		switch s {
		case store.ScopeIMAP, store.ScopeSMTP:
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	if len(out) == 2 {
		return nil // both = no restriction
	}
	return out
}
