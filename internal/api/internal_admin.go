package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"
)

// Internal admin (/internal/v1/admin/*) — the ara server's admin console
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
		// /api/admin/me answers "which person is this" — a service call has none.
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

// ── Spam screening (read-only) ──────────────────────────────

// SpamSetting is the inbound screening configuration. It comes from env at
// boot (MAIL_RSPAMD_URL, MAIL_GREYLIST*, MAIL_DMARC_ENFORCE, MAIL_DNSBL_ZONE)
// and is shown to admins as is; it is not editable through the API.
type SpamSetting struct {
	RspamdURL     string
	Greylist      bool
	GreylistDelay time.Duration
	DMARCEnforce  bool
	DNSBLZoneList []string
}

// WithSpam registers the screening configuration for GET /api/admin/spam.
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
	Rspamd              spamRspamdDTO `json:"rspamd"`
	Greylist            bool          `json:"greylist"`
	GreylistDelaySecond int           `json:"greylistDelaySecond"`
	DMARCEnforce        bool          `json:"dmarcEnforce"`
	DNSBLZoneList       []string      `json:"dnsblZoneList"`
	// Source tells the screen where to change these: "env" = the deployment.
	Source string `json:"source"`
}

// handleSpamSetting reports the screening switches and pings rspamd.
func (s *Server) handleSpamSetting(w http.ResponseWriter, r *http.Request) {
	sp := s.spamSetting
	out := spamDTO{
		Greylist:            sp.Greylist,
		GreylistDelaySecond: int(sp.GreylistDelay / time.Second),
		DMARCEnforce:        sp.DMARCEnforce,
		DNSBLZoneList:       append([]string{}, sp.DNSBLZoneList...),
		Source:              "env",
	}
	if sp.RspamdURL != "" {
		out.Rspamd = pingRspamd(r.Context(), sp.RspamdURL)
	}
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
