package api

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/krisamin/mail/internal/store"
)

// Address management API — account-owned mail addresses (admin only).
// Users (accounts) appear only via JIT provisioning; address add/delete is
// done by an admin. localPart '*' is the catch-all (every unassigned address
// on that domain delivers to the target account).

type addressDTO struct {
	ID           uuid.UUID `json:"id"`
	DomainID     uuid.UUID `json:"domainId"`
	DomainName   string    `json:"domainName"`
	LocalPart    string    `json:"localPart"` // '*' = catch-all
	AccountID    uuid.UUID `json:"accountId"`
	AccountEmail string    `json:"accountEmail"`
	CreatedAt    string    `json:"createdAt"`
}

func toAddressDTO(a *store.Address) addressDTO {
	return addressDTO{
		ID: a.ID, DomainID: a.DomainID, DomainName: a.DomainName,
		LocalPart: a.LocalPart, AccountID: a.AccountID,
		AccountEmail: a.AccountEmail,
		CreatedAt:    a.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

// handleListDomainAddress lists a domain's addresses.
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

// handleListAccountAddress lists an account's addresses.
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

// handleCreateAddress attaches an address to an account (domain-path form).
// body: {localPart, accountId}.
func (s *Server) handleCreateAddress(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		LocalPart string    `json:"localPart"`
		AccountID uuid.UUID `json:"accountId"`
	}
	if err := decodeBody(r, &req); err != nil || req.AccountID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "invalid body (localPart, accountId required)")
		return
	}
	s.createAddress(w, r, id, req.LocalPart, req.AccountID)
}

// handleCreateAccountAddress attaches an address to an account (account-path
// form — for the account page's [local]@[domain select] UX). body: {localPart, domainId}.
func (s *Server) handleCreateAccountAddress(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		LocalPart string    `json:"localPart"`
		DomainID  uuid.UUID `json:"domainId"`
	}
	if err := decodeBody(r, &req); err != nil || req.DomainID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "invalid body (localPart, domainId required)")
		return
	}
	s.createAddress(w, r, req.DomainID, req.LocalPart, id)
}

// createAddress is the shared body of both handlers — verifies the account exists, then creates.
func (s *Server) createAddress(w http.ResponseWriter, r *http.Request, domainID uuid.UUID, localPart string, accountID uuid.UUID) {
	if _, err := s.store.FindAccountByID(r.Context(), accountID); err != nil {
		mapStoreErr(w, err)
		return
	}
	a, err := s.store.CreateAddress(r.Context(), domainID, localPart, accountID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toAddressDTO(a))
}

// handleDeleteAddress deletes an address (the last regular address is a 400).
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
