package api

import (
	"net/http"

	"github.com/google/uuid"
)

// Account overview API — accounts + addresses + app passwords in one response.
// Replaces the admin UI's per-account fan-out (2 requests per account) with a
// single round-trip: 3 store queries total, grouped in memory.

type accountOverviewDTO struct {
	Account         accountDTO       `json:"account"`
	AddressList     []addressDTO     `json:"addressList"`
	AppPasswordList []appPasswordDTO `json:"appPasswordList"`
}

func (s *Server) handleAccountOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	accountList, err := s.store.ListAccount(ctx)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	addressList, err := s.store.ListAllAddress(ctx)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	appPasswordList, err := s.store.ListAllAppPassword(ctx)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	usageMap, err := s.store.ListAccountUsage(ctx)
	if err != nil {
		mapStoreErr(w, err)
		return
	}

	addressMap := map[uuid.UUID][]addressDTO{}
	for _, a := range addressList {
		addressMap[a.AccountID] = append(addressMap[a.AccountID], toAddressDTO(a))
	}
	passwordMap := map[uuid.UUID][]appPasswordDTO{}
	for _, p := range appPasswordList {
		passwordMap[p.AccountID] = append(passwordMap[p.AccountID], toAppPasswordDTO(p))
	}

	out := make([]accountOverviewDTO, 0, len(accountList))
	for _, u := range accountList {
		entry := accountOverviewDTO{
			Account:         toAccountDTO(u),
			AddressList:     addressMap[u.ID],
			AppPasswordList: passwordMap[u.ID],
		}
		entry.Account.UsageBytes = usageMap[u.ID]
		if entry.AddressList == nil {
			entry.AddressList = []addressDTO{}
		}
		if entry.AppPasswordList == nil {
			entry.AppPasswordList = []appPasswordDTO{}
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, out)
}
