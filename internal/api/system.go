package api

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strings"
	"time"
)

// 서버 점검 (/api/admin/system) — 운영 중 상태를 한 화면에서 확인한다.
//   - 프로토콜 포트가 실제로 열려 있고 응답하는지 (self-dial + 배너 확인)
//   - DB 연결/지연
//   - 발송 큐 상태별 건수
//   - 프로세스 가동 시간
//
// 포트 점검은 서버 내부에서 자기 리스너로 접속하는 것 — 프로세스가 정말
// listen 중인지와 프로토콜 배너까지 확인한다. 방화벽/LB 바깥 도달성은
// 여기서 알 수 없으므로 결과에 명시한다 (외부 점검은 클라이언트에서).

// SystemPort는 점검 대상 리스너 하나를 기술한다.
type SystemPort struct {
	Name  string // "imap" | "smtp" | "submission"
	Addr  string // listen 주소 (":1143" 등)
	Kind  string // "imap" | "smtp" — 배너 프로토콜
	TLS   bool   // implicit TLS 여부 (배너 확인 생략, 연결만)
	Check bool   // false면 결과에서 제외
}

// WithSystemPort는 점검 대상 포트 목록을 등록한다 (main.go에서 조립).
func (s *Server) WithSystemPort(portList []SystemPort) *Server {
	s.systemPortList = portList
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

// handleSystemCheck는 포트/DB/큐 상태를 모아 돌려준다.
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

	// ── 발송 큐
	var queueStatus map[string]any
	if stats, err := s.store.OutboundStats(ctx); err == nil {
		queueStatus = map[string]any{"ok": true, "stats": stats}
	} else {
		queueStatus = map[string]any{"ok": false, "error": err.Error()}
	}

	// ── 포트 self-dial
	portResultList := make([]portCheckDTO, 0, len(s.systemPortList))
	for _, p := range s.systemPortList {
		if !p.Check {
			continue
		}
		portResultList = append(portResultList, checkPort(ctx, p))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"uptime":   time.Since(processStart).Round(time.Second).String(),
		"hostname": s.hostname,
		"db":       dbStatus,
		"queue":    queueStatus,
		"port":     portResultList,
		"note":     "포트 점검은 서버 내부 self-dial — 방화벽/LB 밖 도달성은 별개",
	})
}

// checkPort는 리스너에 접속해 배너까지 확인한다.
func checkPort(ctx context.Context, p SystemPort) portCheckDTO {
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
