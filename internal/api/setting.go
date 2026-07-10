package api

import (
	"errors"
	"net/http"

	"github.com/krisamin/mail/internal/store"
)

// 전역 설정 API — 첫 항목은 웹 UI 표시 언어.
//
// locale 값: "auto"(브라우저 언어 감지) | "ko" | "en" | "ja".
// 읽기는 인증 불필요 — 로그인 전 화면(홈/에러)도 언어가 필요하고,
// 값 자체가 민감하지 않다. 쓰기는 admin 전용.

const settingLocaleKey = "locale"

var validLocaleMap = map[string]bool{"auto": true, "ko": true, "en": true, "ja": true}

// handleGetLocale은 전역 표시 언어를 돌려준다 (미설정 = "auto").
func (s *Server) handleGetLocale(w http.ResponseWriter, r *http.Request) {
	value, err := s.store.GetSetting(r.Context(), settingLocaleKey)
	if errors.Is(err, store.ErrNotFound) {
		value = "auto"
	} else if err != nil {
		mapStoreErr(w, err)
		return
	}
	if !validLocaleMap[value] {
		value = "auto" // DB에 이상 값이 들어가도 안전하게
	}
	writeJSON(w, http.StatusOK, map[string]string{"locale": value})
}

// handleSetLocale은 전역 표시 언어를 저장한다 (admin 전용).
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
