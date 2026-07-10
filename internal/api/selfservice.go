package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/krisamin/mail/internal/store"
	"github.com/krisamin/mail/internal/store/postgres"
)

// Self-service API (/api/me/*) — a logged-in user manages *their own*
// account and app passwords. No admin group required.
//
// Account mapping: OIDC sub claim == account identity (0006). Accounts are
// created by JIT provisioning (/api/me/provision) on first login — only when
// the email domain is registered on the server. Address add/delete is admin-only.
//
// Ownership: listing/issuing queries only the caller's account, and revoke
// verifies the target password belongs to the caller first (IDOR prevention).

// resolveMe finds the caller's account by the token's sub claim.
// On a sub miss it also tries the email-owned address (pre-provisioning reads).
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
	// no account → not provisioned yet
	writeError(w, http.StatusNotFound, "account not provisioned for "+id.Subject)
	return nil
}

// handleMeAccount summarizes the caller's account.
func (s *Server) handleMeAccount(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	writeJSON(w, http.StatusOK, toAccountDTO(u))
}

// handleMeProvision is JIT provisioning — the RR7 callback calls it on first login.
// Creates the account if missing (with primary address + INBOX when the email
// domain is registered; bare account otherwise — no address = no mail).
// If it exists, only refreshes the email (idempotent). Login is always allowed
// regardless of domain registration — admin is judged by OIDC group, so the
// admin screens are reachable even on an empty DB.
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

// handleMeAddress lists the caller's addresses (what they can receive/send as).
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

// handleMeListAppPassword lists the caller's app passwords.
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

// handleMeCreateAppPassword issues an app password for the caller (plaintext shown once).
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
		"plaintext":   plain, // this response only — never stored
	})
}

// handleMeRevokeAppPassword revokes after verifying ownership (IDOR prevention).
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
	// ownership check: only ids present in the caller's own list
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
