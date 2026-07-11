package api

import (
	"net/http"
	"strings"

	"github.com/krisamin/mail/internal/store"
)

// Filter rule API (/api/me/filter) — the signed-in user manages their own
// delivery rules. Rules run on INBOX-bound delivery in position order; the
// first match wins. Spam/DMARC quarantine beats filters (see smtp.deliver).

type filterRuleDTO struct {
	ID            int64  `json:"id"`
	Position      int    `json:"position"`
	Name          string `json:"name"`
	Active        bool   `json:"active"`
	Field         string `json:"field"`
	HeaderName    string `json:"headerName"`
	MatchType     string `json:"matchType"`
	Pattern       string `json:"pattern"`
	Action        string `json:"action"`
	ActionMailbox string `json:"actionMailbox"`
	CreatedAt     string `json:"createdAt"`
}

func toFilterRuleDTO(r *store.FilterRule) filterRuleDTO {
	return filterRuleDTO{
		ID: r.ID, Position: r.Position, Name: r.Name, Active: r.Active,
		Field: r.Field, HeaderName: r.HeaderName,
		MatchType: r.MatchType, Pattern: r.Pattern,
		Action: r.Action, ActionMailbox: r.ActionMailbox,
		CreatedAt: r.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

// filterRuleBody is the create/update payload.
type filterRuleBody struct {
	Name          string `json:"name"`
	Active        *bool  `json:"active"`
	Field         string `json:"field"`
	HeaderName    string `json:"headerName"`
	MatchType     string `json:"matchType"`
	Pattern       string `json:"pattern"`
	Action        string `json:"action"`
	ActionMailbox string `json:"actionMailbox"`
}

func (b *filterRuleBody) toRule(accountID int64) *store.FilterRule {
	active := true
	if b.Active != nil {
		active = *b.Active
	}
	return &store.FilterRule{
		AccountID:     accountID,
		Name:          strings.TrimSpace(b.Name),
		Active:        active,
		Field:         b.Field,
		HeaderName:    strings.TrimSpace(b.HeaderName),
		MatchType:     b.MatchType,
		Pattern:       strings.TrimSpace(b.Pattern),
		Action:        b.Action,
		ActionMailbox: strings.TrimSpace(b.ActionMailbox),
	}
}

func (s *Server) handleMeListFilter(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	ruleList, err := s.store.ListFilterRule(r.Context(), u.ID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	out := make([]filterRuleDTO, 0, len(ruleList))
	for _, rule := range ruleList {
		out = append(out, toFilterRuleDTO(rule))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleMeCreateFilter(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	var body filterRuleBody
	if err := decodeBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	rule, err := s.store.CreateFilterRule(r.Context(), body.toRule(u.ID))
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toFilterRuleDTO(rule))
}

func (s *Server) handleMeUpdateFilter(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body filterRuleBody
	if err := decodeBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	rule := body.toRule(u.ID)
	rule.ID = id
	if err := s.store.UpdateFilterRule(r.Context(), rule); err != nil {
		mapStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMeDeleteFilter(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteFilterRule(r.Context(), u.ID, id); err != nil {
		mapStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMeMoveFilter reorders a rule one step. body: {"direction": -1|1}.
func (s *Server) handleMeMoveFilter(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		Direction int `json:"direction"`
	}
	if err := decodeBody(r, &req); err != nil || (req.Direction != -1 && req.Direction != 1) {
		writeError(w, http.StatusBadRequest, "invalid body (direction -1 or 1)")
		return
	}
	if err := s.store.SwapFilterRule(r.Context(), u.ID, id, req.Direction); err != nil {
		mapStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
