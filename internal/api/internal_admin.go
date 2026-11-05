package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/krisamin/mail/internal/spam"
)

// Internal admin (/internal/v1/admin/*): the ara server's admin console
// drives maild through this. It is the same handler set as /api/admin/*
// (one source of behavior), reached with the service token instead of a
// person's OIDC token: ara checks its own admin role before it calls.
// /internal/v1/admin/<rest> is served as /api/admin/<rest>.

const internalAdminPrefix = "/internal/v1/admin/"

// internalAdmin rewrites the path onto the admin mux. The service token was
// already checked by requireServiceToken; no X-Mail-Account is needed
// because admin calls name their target in the path or body.
func (s *Server) internalAdmin() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, internalAdminPrefix)
		// /api/admin/me answers "which person is this"; a service call has none.
		if rest == "me" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/api/admin/" + rest
		r2.URL.RawPath = ""
		s.admin.ServeHTTP(w, r2)
	})
}

// -- Spam screening --------------------------------------------------

// SpamSetting wires the screening policy into the admin API. Source holds
// the env default and the admin-saved policy (the SMTP backend reads the
// same Source, so a save applies to the next connection).
type SpamSetting struct {
	RspamdURL string
	Source    *spam.Source
}

// WithSpam registers the screening policy for /api/admin/spam.
func (s *Server) WithSpam(sp SpamSetting) *Server {
	s.spamSetting = sp
	return s
}

type spamRspamdDTO struct {
	Configured bool   `json:"configured"`
	Reachable  bool   `json:"reachable"`
	Latency    string `json:"latency,omitempty"`
	Error      string `json:"error,omitempty"`
}

type spamDTO struct {
	Rspamd spamRspamdDTO `json:"rspamd"`
	// Policy is what applies now; Default is the env policy a reset returns to.
	Policy  spam.Policy `json:"policy"`
	Default spam.Policy `json:"default"`
	// Source: "setting" = an admin saved Policy, "env" = the default applies.
	Source string `json:"source"`
}

func (s *Server) spamView(ctx context.Context, ping bool) (spamDTO, bool) {
	src := s.spamSetting.Source
	if src == nil {
		return spamDTO{}, false
	}
	snap, saved := src.Current(ctx)
	out := spamDTO{Policy: snap.Policy, Default: src.Default(), Source: "env"}
	if saved {
		out.Source = "setting"
	}
	if out.Policy.DNSBLZoneList == nil {
		out.Policy.DNSBLZoneList = []string{}
	}
	if out.Default.DNSBLZoneList == nil {
		out.Default.DNSBLZoneList = []string{}
	}
	if s.spamSetting.RspamdURL != "" {
		out.Rspamd.Configured = true
		if ping {
			out.Rspamd = pingRspamd(ctx, s.spamSetting.RspamdURL)
		}
	}
	return out, true
}

// handleSpamSetting: GET /api/admin/spam, the policy in force and rspamd's ping.
func (s *Server) handleSpamSetting(w http.ResponseWriter, r *http.Request) {
	out, ok := s.spamView(r.Context(), true)
	if !ok {
		writeError(w, http.StatusNotFound, "spam policy not configured")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSetSpamPolicy: PUT /api/admin/spam with a whole Policy. It is stored
// (setting spam.policy) and applies to the next SMTP connection.
func (s *Server) handleSetSpamPolicy(w http.ResponseWriter, r *http.Request) {
	src := s.spamSetting.Source
	if src == nil {
		writeError(w, http.StatusNotFound, "spam policy not configured")
		return
	}
	var req spam.Policy
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	norm, err := req.Normalize()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if norm.Rspamd && !src.RspamdAvailable() {
		writeError(w, http.StatusBadRequest, "rspamd is not configured on this server (MAIL_RSPAMD_URL)")
		return
	}
	raw, _ := json.Marshal(norm)
	if err := s.store.SetSetting(r.Context(), spam.PolicySettingKey, string(raw)); err != nil {
		mapStoreErr(w, err)
		return
	}
	src.Invalidate()
	out, _ := s.spamView(r.Context(), false)
	writeJSON(w, http.StatusOK, out)
}

// handleResetSpamPolicy: DELETE /api/admin/spam drops the saved policy, so
// the env default applies again.
func (s *Server) handleResetSpamPolicy(w http.ResponseWriter, r *http.Request) {
	src := s.spamSetting.Source
	if src == nil {
		writeError(w, http.StatusNotFound, "spam policy not configured")
		return
	}
	if err := s.store.DeleteSetting(r.Context(), spam.PolicySettingKey); err != nil {
		mapStoreErr(w, err)
		return
	}
	src.Invalidate()
	out, _ := s.spamView(r.Context(), false)
	writeJSON(w, http.StatusOK, out)
}

// pingRspamd calls rspamd's /ping (answers "pong") with a short timeout.
func pingRspamd(ctx context.Context, base string) spamRspamdDTO {
	out := spamRspamdDTO{Configured: true}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/ping", nil)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	out.Latency = time.Since(start).Round(time.Microsecond).String()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(strings.TrimSpace(string(body)), "pong") {
		out.Error = "unexpected answer: " + resp.Status
		return out
	}
	out.Reachable = true
	return out
}
