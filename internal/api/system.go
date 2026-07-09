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

// 서버 점검 (/api/admin/system) — 운영 중 상태를 한 화면에서 확인한다.
//   - 내부 리스너: 데몬이 실제 listen 중이고 배너가 정상인지 (self-dial)
//   - 외부 도달성: MAIL_HOSTNAME의 공인 경로로 표준 포트에 접속되는지
//     (LB/라우터 포워딩까지 뚫려야 성공 — 실제 클라이언트가 겪는 경로)
//   - DB 연결/지연, 발송 큐 상태, 프로세스 가동 시간
//
// 내부 self-dial 성공 ≠ 외부에서 접속 가능. 두 점검을 분리해서 보여준다.
// 외부 점검도 pod에서 나가는 것이라 헤어핀 NAT이 안 되는 라우터에선
// 실제 외부와 다르게 나올 수 있다 — 결과에 명시.

// SystemPort는 점검 대상 리스너 하나를 기술한다.
type SystemPort struct {
	Name  string // "imap" | "smtp" | "submission"
	Addr  string // listen 주소 (":1143" 등)
	Kind  string // "imap" | "smtp" — 배너 프로토콜
	TLS   bool   // implicit TLS 여부 (배너 확인 생략, 연결만)
	Check bool   // false면 결과에서 제외
}

// ExternalPort는 외부 도달성 점검 대상 — 클라이언트가 쓰는 표준 포트.
type ExternalPort struct {
	Name string // "imaps(993)" 등 표시용
	Port string // "993"
	// Mode: "tls" = implicit TLS 핸드셰이크까지, "banner" = 평문 배너 읽기
	Mode string
}

// WithSystemPort는 내부 리스너 점검 목록을 등록한다 (main.go에서 조립).
func (s *Server) WithSystemPort(portList []SystemPort) *Server {
	s.systemPortList = portList
	return s
}

// WithExternalPort는 외부 도달성 점검 목록을 등록한다.
// host는 보통 MAIL_HOSTNAME (mail.krisam.in).
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

// handleSystemCheck는 리스너/외부 도달성/DB/큐 상태를 모아 돌려준다.
func (s *Server) handleSystemCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
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

	// ── 발송 큐
	var queueStatus map[string]any
	if stats, err := s.store.OutboundStats(ctx); err == nil {
		queueStatus = map[string]any{"ok": true, "stats": stats}
	} else {
		queueStatus = map[string]any{"ok": false, "error": err.Error()}
	}

	// ── 내부 리스너 self-dial
	listenerList := make([]portCheckDTO, 0, len(s.systemPortList))
	for _, p := range s.systemPortList {
		if !p.Check {
			continue
		}
		listenerList = append(listenerList, checkListener(ctx, p))
	}

	// ── 외부 도달성 (표준 포트, 공인 경로) — 병렬로
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
		"uptime":   time.Since(processStart).Round(time.Second).String(),
		"hostname": s.hostname,
		"db":       dbStatus,
		"queue":    queueStatus,
		"listener": listenerList,
		"external": externalList,
		"externalHost": s.externalHost,
		"note": "리스너=프로세스 내부 self-dial (데몬 정상 여부만). " +
			"외부 도달성=공인 호스트네임으로 실접속 (LB/라우터 포워딩 포함 — " +
			"단, 서버에서 나가는 경로라 헤어핀 NAT 미지원 라우터에선 오탐 가능).",
	})
}

// checkListener는 내부 리스너에 접속해 배너까지 확인한다.
func checkListener(ctx context.Context, p SystemPort) portCheckDTO {
	out := portCheckDTO{Name: p.Name, Addr: p.Addr}

	// listen 주소(":1143")를 다이얼 주소("127.0.0.1:1143")로
	host, port, err := net.SplitHostPort(p.Addr)
	if err != nil {
		out.Error = "주소 파싱 실패: " + err.Error()
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

	// 배너 읽기 — implicit TLS 리스너는 TLS 핸드셰이크 전에 배너가 없으니 생략
	if !p.TLS {
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if line, err := bufio.NewReader(conn).ReadString('\n'); err == nil {
			out.Banner = strings.TrimSpace(line)
		}
	}
	out.Latency = time.Since(start).Round(time.Microsecond).String()
	return out
}

// checkExternal은 공인 호스트네임 + 표준 포트로 실제 접속을 시도한다.
//   - mode "tls": TCP + TLS 핸드셰이크 (인증서 검증 포함 — 만료/이름 불일치 감지)
//   - mode "banner": TCP + 프로토콜 배너 한 줄
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
			out.Error = "TLS 핸드셰이크 실패: " + err.Error()
			return out
		}
		cert := tconn.ConnectionState().PeerCertificates
		if len(cert) > 0 {
			out.Banner = "TLS OK · " + cert[0].Subject.CommonName +
				" (만료 " + cert[0].NotAfter.Format("2006-01-02") + ")"
		}
	default: // banner
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			out.Error = "배너 읽기 실패: " + err.Error()
			return out
		}
		out.Banner = strings.TrimSpace(line)
	}
	out.Open = true
	out.Latency = time.Since(start).Round(time.Millisecond).String()
	return out
}
