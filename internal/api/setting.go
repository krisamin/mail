package api

import (
	"errors"
	"net/http"

	"github.com/krisamin/mail/internal/store"
)

// Global settings API — the first entry is the web UI display locale.
//
// locale values: "auto" (browser language detection) | "ko" | "en" | "ja".
// Reads need no auth — pre-login screens (home/error) need the locale too,
// and the value itself isn't sensitive. Writes are admin-only.

const settingLocaleKey = "locale"

var validLocaleMap = map[string]bool{"auto": true, "ko": true, "en": true, "ja": true}

// handleGetLocale returns the global display locale (unset = "auto").
func (s *Server) handleGetLocale(w http.ResponseWriter, r *http.Request) {
	value, err := s.store.GetSetting(r.Context(), settingLocaleKey)
	if errors.Is(err, store.ErrNotFound) {
		value = "auto"
	} else if err != nil {
		mapStoreErr(w, err)
		return
	}
	if !validLocaleMap[value] {
		value = "auto" // stay safe even if a bad value lands in the DB
	}
	writeJSON(w, http.StatusOK, map[string]string{"locale": value})
}

// handleSetLocale stores the global display locale (admin only).
func (s *Server) handleSetLocale(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Locale string `json:"locale"`
	}
	if err := decodeBody(r, &req); err != nil || !validLocaleMap[req.Locale] {
		writeError(w, http.StatusBadRequest, "invalid body (locale must be auto|ko|en|ja)")
		return
	}
	if err := s.store.SetSetting(r.Context(), settingLocaleKey, req.Locale); err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"locale": req.Locale})
}
