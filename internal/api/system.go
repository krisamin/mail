package api

import (
	"bufio"
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"time"
)

// Server check (/api/admin/system) — view operational status on one screen.
//   - Internal listeners: whether the daemon is actually listening and the banner is healthy (self-dial)
//   - External reachability: whether the standard ports are reachable via MAIL_HOSTNAME's
//     public path (LB/router forwarding must be open too — the path real clients experience)
//   - DB connectivity/latency, outbound queue status, process uptime
//
// Internal self-dial success ≠ reachable from outside. The two checks are shown separately.
// The external check also originates from the pod, so on routers without hairpin NAT
// it may differ from the real outside view — noted in the result.

// SystemPort describes one listener to check.
type SystemPort struct {
	Name  string // "imap" | "smtp" | "submission"
	Addr  string // listen address (e.g. ":1143")
	Kind  string // "imap" | "smtp" — banner protocol
	TLS   bool   // whether implicit TLS (skip banner check, connect only)
	Check bool   // false excludes it from the results
}

// ExternalPort is an external reachability check target — the standard ports clients use.
type ExternalPort struct {
	Name string // display label, e.g. "imaps(993)"
	Port string // "993"
	// Mode: "tls" = up to the implicit TLS handshake, "banner" = read the plaintext banner
	Mode string
}

// WithSystemPort registers the internal listener check list (assembled in main.go).
func (s *Server) WithSystemPort(portList []SystemPort) *Server {
	s.systemPortList = portList
	return s
}

// WithExternalPort registers the external reachability check list.
// host is usually MAIL_HOSTNAME (mail.krisam.in).
func (s *Server) WithExternalPort(host string, portList []ExternalPort) *Server {
	s.externalHost = host
	s.externalPortList = portList
	return s
}

var processStart = time.Now()

type portCheckDTO struct {
	Name    string `json:"name"`
	Addr    string `json:"addr"`
	Open    bool   `json:"open"`
	Banner  string `json:"banner,omitempty"`
	Latency string `json:"latency,omitempty"`
	Error   string `json:"error,omitempty"`
}

// handleSystemCheck gathers listener/DB/queue status (fast checks only).
// External reachability is slow (blocked ports wait for timeout), so it's a separate endpoint.
func (s *Server) handleSystemCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// ── DB ping
	dbStart := time.Now()
	dbErr := s.store.Pool().Ping(ctx)
	dbLatency := time.Since(dbStart)
	dbStatus := map[string]any{
		"ok":      dbErr == nil,
		"latency": dbLatency.Round(time.Microsecond).String(),
	}
	if dbErr != nil {
		dbStatus["error"] = dbErr.Error()
	}

	// ── Outbound queue
	var queueStatus map[string]any
	if statMap, err := s.store.OutboundStat(ctx); err == nil {
		queueStatus = map[string]any{"ok": true, "statMap": statMap}
	} else {
		queueStatus = map[string]any{"ok": false, "error": err.Error()}
	}

	// ── Internal listener self-dial (local, so immediate)
	listenerList := make([]portCheckDTO, 0, len(s.systemPortList))
	for _, p := range s.systemPortList {
		if !p.Check {
			continue
		}
		listenerList = append(listenerList, checkListener(ctx, p))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"uptime":       time.Since(processStart).Round(time.Second).String(),
		"hostname":     s.hostname,
		"db":           dbStatus,
		"queue":        queueStatus,
		"listener":     listenerList,
		"externalHost": s.externalHost,
		"note": "listener = in-process self-dial (only whether the daemon is healthy). " +
			"External reachability is at /api/admin/system/external (separate — blocked ports wait until timeout).",
	})
}

// handleSystemExternal checks external reachability only (slow — blocked ports
// wait up to the 5s dial timeout. Runs in parallel, so at most ~5s).
func (s *Server) handleSystemExternal(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	externalList := make([]portCheckDTO, len(s.externalPortList))
	if s.externalHost != "" && len(s.externalPortList) > 0 {
		done := make(chan struct{})
		for i, p := range s.externalPortList {
			go func(i int, p ExternalPort) {
				externalList[i] = checkExternal(ctx, s.externalHost, p)
				done <- struct{}{}
			}(i, p)
		}
		for range s.externalPortList {
			<-done
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"externalHost": s.externalHost,
		"external":     externalList,
		"note": "real connection via the public hostname (including LB/router forwarding) — " +
			"however, since the path originates from the server, routers without hairpin NAT may give false results.",
	})
}

// checkListener connects to an internal listener and verifies up to the banner.
func checkListener(ctx context.Context, p SystemPort) portCheckDTO {
	out := portCheckDTO{Name: p.Name, Addr: p.Addr}

	// Turn the listen address (":1143") into a dial address ("127.0.0.1:1143")
	host, port, err := net.SplitHostPort(p.Addr)
	if err != nil {
		out.Error = "address parsing failed: " + err.Error()
		return out
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	start := time.Now()
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer conn.Close()
	out.Open = true

	// Read the banner — implicit TLS listeners have no banner before the TLS handshake, so skip
	if !p.TLS {
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if line, err := bufio.NewReader(conn).ReadString('\n'); err == nil {
			out.Banner = strings.TrimSpace(line)
		}
	}
	out.Latency = time.Since(start).Round(time.Microsecond).String()
	return out
}

// checkExternal attempts a real connection via the public hostname + standard port.
//   - mode "tls": TCP + TLS handshake (including certificate validation — detects expiry/name mismatch)
//   - mode "banner": TCP + one line of protocol banner
func checkExternal(ctx context.Context, host string, p ExternalPort) portCheckDTO {
	out := portCheckDTO{Name: p.Name, Addr: net.JoinHostPort(host, p.Port)}

	start := time.Now()
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", out.Addr)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer conn.Close()

	switch p.Mode {
	case "tls":
		tconn := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		tconn.SetDeadline(time.Now().Add(5 * time.Second))
		if err := tconn.HandshakeContext(ctx); err != nil {
			out.Error = "TLS handshake failed: " + err.Error()
			return out
		}
		cert := tconn.ConnectionState().PeerCertificates
		if len(cert) > 0 {
			out.Banner = "TLS OK · " + cert[0].Subject.CommonName +
				" (expires " + cert[0].NotAfter.Format("2006-01-02") + ")"
		}
	default: // banner
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			out.Error = "banner read failed: " + err.Error()
			return out
		}
		out.Banner = strings.TrimSpace(line)
	}
	out.Open = true
	out.Latency = time.Since(start).Round(time.Millisecond).String()
	return out
}
