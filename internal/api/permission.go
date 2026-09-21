package api

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/krisamin/mail/internal/policy"
	"github.com/krisamin/mail/internal/store"
)

// Permission endpoints (0005).
//
// Permissions live on groups. The default group is the floor everybody
// stands on; groups above it add (or take away) where they have an opinion,
// and the highest opinion wins. On top of that the domain keeps its own kill
// switch for crossing the server boundary.

type groupDTO struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Position  int       `json:"position"`
	IsDefault bool      `json:"isDefault"`

	CanSend            string `json:"canSend"`
	CanSendExternal    string `json:"canSendExternal"`
	CanReceiveExternal string `json:"canReceiveExternal"`
	DailySendLimit     *int   `json:"dailySendLimit"`

	MemberIDList []uuid.UUID `json:"memberIdList"`
	CreatedAt    string      `json:"createdAt"`
}

func toGroupDTO(g *store.AccountGroup) groupDTO {
	memberIDList := g.MemberIDList
	if memberIDList == nil {
		memberIDList = []uuid.UUID{}
	}
	return groupDTO{
		ID: g.ID, Name: g.Name, Position: g.Position, IsDefault: g.IsDefault,
		CanSend:            g.CanSend,
		CanSendExternal:    g.CanSendExternal,
		CanReceiveExternal: g.CanReceiveExternal,
		DailySendLimit:     g.DailySendLimit,
		MemberIDList:       memberIDList,
		CreatedAt:          g.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

// effectiveDTO is what a group stack adds up to for one account.
type effectiveDTO struct {
	CanSend            bool     `json:"canSend"`
	CanSendExternal    bool     `json:"canSendExternal"`
	CanReceiveExternal bool     `json:"canReceiveExternal"`
	DailySendLimit     *int     `json:"dailySendLimit"`
	GroupList          []string `json:"groupList"`
	SentToday          int      `json:"sentToday"`
}

func toEffectiveDTO(e policy.Effective) effectiveDTO {
	groupList := e.GroupList
	if groupList == nil {
		groupList = []string{}
	}
	return effectiveDTO{
		CanSend:            e.CanSend,
		CanSendExternal:    e.CanSendExternal,
		CanReceiveExternal: e.CanReceiveExternal,
		DailySendLimit:     e.DailySendLimit,
		GroupList:          groupList,
	}
}

// GET /api/admin/group
func (s *Server) handleListGroup(w http.ResponseWriter, r *http.Request) {
	groupList, err := s.store.ListAccountGroup(r.Context())
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	out := make([]groupDTO, 0, len(groupList))
	for _, g := range groupList {
		out = append(out, toGroupDTO(g))
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /api/admin/group
func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	g, err := s.store.CreateAccountGroup(r.Context(), req.Name)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toGroupDTO(g))
}

// PATCH /api/admin/group/{id}
func (s *Server) handlePatchGroup(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		Name               string `json:"name"`
		CanSend            string `json:"canSend"`
		CanSendExternal    string `json:"canSendExternal"`
		CanReceiveExternal string `json:"canReceiveExternal"`
		DailySendLimit     *int   `json:"dailySendLimit"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	limit := req.DailySendLimit
	if limit != nil && *limit <= 0 {
		limit = nil
	}
	g, err := s.store.UpdateAccountGroup(r.Context(), id, store.GroupPermission{
		Name:               req.Name,
		CanSend:            req.CanSend,
		CanSendExternal:    req.CanSendExternal,
		CanReceiveExternal: req.CanReceiveExternal,
		DailySendLimit:     limit,
	})
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toGroupDTO(g))
}

// DELETE /api/admin/group/{id}
func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteAccountGroup(r.Context(), id); err != nil {
		mapStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/admin/group/{id}/move
func (s *Server) handleMoveGroup(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		Up bool `json:"up"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.store.MoveAccountGroup(r.Context(), id, req.Up); err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// PUT /api/admin/group/{id}/member — replaces the whole membership list.
func (s *Server) handleSetGroupMember(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		AccountIDList []string `json:"accountIdList"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	idList := make([]uuid.UUID, 0, len(req.AccountIDList))
	for _, raw := range req.AccountIDList {
		accountID, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid account id")
			return
		}
		idList = append(idList, accountID)
	}
	if err := s.store.SetAccountGroupMember(r.Context(), id, idList); err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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

// GET /api/me/permission — what the signed-in person may do, which groups
// said so, and how much of today's allowance is gone. Read-only: nobody
// widens their own permissions here.
func (s *Server) handleMePermission(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	groupList, err := s.store.GroupListForAccount(r.Context(), u.ID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	dto := toEffectiveDTO(policy.Resolve(groupList))
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
