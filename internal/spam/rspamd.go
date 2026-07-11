package spam

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

// rspamd HTTP scan client (checkv2 API).
//
// maild streams the received message to rspamd's /checkv2 endpoint and maps
// the returned action to a delivery decision:
//
//	reject                    → 554 refuse at DATA
//	add header / greylist /
//	soft reject / rewrite     → quarantine to Junk (we don't mutate bodies)
//	no action                 → deliver normally
//
// Everything is fail-open: a scanner outage must never take mail down.

// scanTimeout bounds one scan round-trip. rspamd typically answers in tens
// of milliseconds; big attachments can take a couple of seconds.
const scanTimeout = 8 * time.Second

// ScanAction is the normalized decision.
type ScanAction int

const (
	// ScanPass delivers normally (also used on scanner errors — fail-open).
	ScanPass ScanAction = iota
	// ScanJunk quarantines to the Junk folder.
	ScanJunk
	// ScanReject refuses the message (554).
	ScanReject
)

// ScanResult is the mapped rspamd verdict.
type ScanResult struct {
	Action ScanAction
	// Score/RequiredScore/RawAction are for logging.
	Score         float64
	RequiredScore float64
	RawAction     string
}

// Scanner talks to one rspamd instance.
type Scanner struct {
	url      string // base URL, e.g. http://rspamd:11333
	password string // optional shared secret (Password header)
	client   *http.Client
}

// NewScanner creates an rspamd scanner client.
func NewScanner(url, password string) *Scanner {
	return &Scanner{
		url:      url,
		password: password,
		client:   &http.Client{Timeout: scanTimeout},
	}
}

// checkResponse is the subset of the /checkv2 reply we use.
type checkResponse struct {
	Action        string  `json:"action"`
	Score         float64 `json:"score"`
	RequiredScore float64 `json:"required_score"`
}

// Scan submits a message. Context headers (IP/HELO/from/rcpt) let rspamd run
// its network rules too. Returns ScanPass on any error (fail-open).
func (s *Scanner) Scan(ctx context.Context, raw []byte, clientIP net.IP, helo, from string, rcptList []string) (ScanResult, error) {
	ctx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.url+"/checkv2", bytes.NewReader(raw))
	if err != nil {
		return ScanResult{Action: ScanPass}, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if s.password != "" {
		req.Header.Set("Password", s.password)
	}
	if clientIP != nil {
		req.Header.Set("IP", clientIP.String())
	}
	if helo != "" {
		req.Header.Set("Helo", helo)
	}
	if from != "" {
		req.Header.Set("From", from)
	}
	for _, rcpt := range rcptList {
		req.Header.Add("Rcpt", rcpt)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return ScanResult{Action: ScanPass}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ScanResult{Action: ScanPass}, fmt.Errorf("rspamd status %d", resp.StatusCode)
	}
	var body checkResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return ScanResult{Action: ScanPass}, err
	}

	result := ScanResult{
		Score: body.Score, RequiredScore: body.RequiredScore, RawAction: body.Action,
	}
	switch body.Action {
	case "reject":
		result.Action = ScanReject
	case "add header", "rewrite subject", "greylist", "soft reject":
		// we don't rewrite bodies/subjects and greylisting is handled
		// upstream — all of these degrade to quarantine
		result.Action = ScanJunk
	default: // "no action", "accept", ...
		result.Action = ScanPass
	}
	return result, nil
}
