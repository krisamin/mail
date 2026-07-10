// Command maild is the mail server daemon.
//
// 한 바이너리에 IMAP(:1143) + SMTP 수신(:2525) + submission(:2587) +
// Admin/셀프서비스 REST API(:8080) + 발송 큐 워커를 조립한다.
// 기동 시 내장 마이그레이션으로 스키마를 수렴시키고, SIGTERM/SIGINT에
// graceful shutdown한다 (k8s rolling update 안전).
package main

import (
	"context"
	"crypto/tls"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/emersion/go-imap/v2/imapserver"
	gosmtp "github.com/emersion/go-smtp"

	"github.com/krisamin/mail/internal/api"
	imapbackend "github.com/krisamin/mail/internal/imap"
	"github.com/krisamin/mail/internal/queue"
	smtpbackend "github.com/krisamin/mail/internal/smtp"
	"github.com/krisamin/mail/internal/store/migration"
	"github.com/krisamin/mail/internal/store/postgres"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// loadTLS는 MAIL_TLS_CERT/MAIL_TLS_KEY가 설정돼 있으면 TLS 설정을 만든다.
// 미설정이면 nil (평문 — 프록시/터널 뒤이거나 dev).
func loadTLS() *tls.Config {
	certFile, keyFile := os.Getenv("MAIL_TLS_CERT"), os.Getenv("MAIL_TLS_KEY")
	if certFile == "" || keyFile == "" {
		return nil
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		log.Fatalf("TLS 인증서 로드 실패: %v", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
}

func main() {
	dsn := os.Getenv("MAIL_DSN")
	if dsn == "" {
		log.Fatal("MAIL_DSN 미설정 (예: postgres://mail:maildev@localhost:55432/mail)")
	}
	// dev 기본값: 143/25/587은 특권 포트 → 1143/2525/2587. k8s에선 Service로 매핑.
	imapAddr := env("MAIL_IMAP_ADDR", ":1143")
	smtpAddr := env("MAIL_SMTP_ADDR", ":2525")
	submissionAddr := env("MAIL_SUBMISSION_ADDR", ":2587")
	hostname := env("MAIL_HOSTNAME", "mail.example.com")
	tlsConfig := loadTLS()

	st, err := postgres.New(context.Background(), dsn)
	if err != nil {
		log.Fatalf("store 연결 실패: %v", err)
	}
	defer st.Close()

	// 내장 마이그레이션 — 빈 DB든 기존 DB든 여기서 스키마 수렴 (k8s에선 이거 하나로 끝)
	if err := migration.Run(context.Background(), st.Pool()); err != nil {
		log.Fatalf("마이그레이션 실패: %v", err)
	}

	errCh := make(chan error, 4)

	// IMAP 서버 — TLS 설정 시 implicit TLS, 미설정 시 평문(프록시/dev 전제)
	imapOpts := &imapserver.Options{
		NewSession: imapbackend.NewBackend(st).NewSession,
		TLSConfig:  tlsConfig,
	}
	if tlsConfig == nil {
		imapOpts.InsecureAuth = true
	}
	imapSrv := imapserver.New(imapOpts)
	go func() {
		if tlsConfig != nil {
			log.Printf("maild: IMAP 서버 시작 %s (TLS)", imapAddr)
			errCh <- imapSrv.ListenAndServeTLS(imapAddr)
			return
		}
		log.Printf("maild: IMAP 서버 시작 %s (평문 — 프록시/dev 전제)", imapAddr)
		errCh <- imapSrv.ListenAndServe(imapAddr)
	}()

	// SMTP 수신(MX) 서버 — SPF/DKIM/DMARC 검증 + 정책 집행(옵션)
	mxBackend := smtpbackend.NewBackend(st, hostname)
	if os.Getenv("MAIL_DMARC_ENFORCE") == "true" {
		mxBackend.WithDMARCEnforcement()
		log.Printf("maild: DMARC 정책 집행 활성 (reject→550, quarantine→Junk)")
	} else {
		mxBackend.WithInboundVerification()
	}
	smtpSrv := gosmtp.NewServer(mxBackend)
	smtpSrv.Addr = smtpAddr
	smtpSrv.Domain = hostname
	smtpSrv.ReadTimeout = 60 * time.Second
	smtpSrv.WriteTimeout = 60 * time.Second
	smtpSrv.MaxMessageBytes = 25 * 1024 * 1024 // 25MB
	smtpSrv.MaxRecipients = 50
	smtpSrv.TLSConfig = tlsConfig // STARTTLS 제공 (설정 시)
	go func() {
		log.Printf("maild: SMTP 수신 서버 시작 %s (hostname=%s)", smtpAddr, hostname)
		errCh <- smtpSrv.ListenAndServe()
	}()

	// SMTP submission 서버 (AUTH 필수 — 앱 비밀번호로 우리 유저가 제출)
	// 발송 relay는 DB에서 관리 (0005) — 외부 도메인 제출은 항상 큐로 받고,
	// 워커가 발송 시점에 relay를 해석한다 (도메인 지정 → default).
	submissionBackend := smtpbackend.NewSubmissionBackend(st, hostname, true)
	subSrv := gosmtp.NewServer(submissionBackend)
	subSrv.Addr = submissionAddr
	subSrv.Domain = hostname
	subSrv.ReadTimeout = 60 * time.Second
	subSrv.WriteTimeout = 60 * time.Second
	subSrv.MaxMessageBytes = 25 * 1024 * 1024
	subSrv.MaxRecipients = 50
	subSrv.TLSConfig = tlsConfig
	// TLS 미설정 시에만 평문 AUTH 허용 (프록시/터널 뒤 전제)
	subSrv.AllowInsecureAuth = tlsConfig == nil
	go func() {
		log.Printf("maild: SMTP submission 서버 시작 %s (STARTTLS=%v)", submissionAddr, tlsConfig != nil)
		errCh <- subSrv.ListenAndServe()
	}()

	// SMTPS submission 서버 (implicit TLS — RFC 8314 권장, 465로 노출).
	// 같은 백엔드 공유 (인증/큐 로직 동일). TLS 인증서 없으면 생략.
	var smtpsSrv *gosmtp.Server
	smtpsAddr := env("MAIL_SMTPS_ADDR", ":2465")
	if tlsConfig != nil {
		smtpsSrv = gosmtp.NewServer(submissionBackend)
		smtpsSrv.Addr = smtpsAddr
		smtpsSrv.Domain = hostname
		smtpsSrv.ReadTimeout = 60 * time.Second
		smtpsSrv.WriteTimeout = 60 * time.Second
		smtpsSrv.MaxMessageBytes = 25 * 1024 * 1024
		smtpsSrv.MaxRecipients = 50
		smtpsSrv.TLSConfig = tlsConfig
		go func() {
			log.Printf("maild: SMTPS submission 서버 시작 %s (implicit TLS)", smtpsAddr)
			errCh <- smtpsSrv.ListenAndServeTLS()
		}()
	}

	// 발송 큐 워커 — relay는 전부 DB에서 관리 (어드민 UI로 추가/변경,
	// 재기동 불필요). relay 미설정 도메인의 발송은 일시 오류로 재시도된다.
	workerCtx, workerCancel := context.WithCancel(context.Background())
	worker := queue.NewWorker(st, queue.NewResolvingSender(st), queue.Config{}).
		WithSigner(queue.NewDKIMSigner(st))
	workerDone := make(chan struct{})
	go func() {
		worker.Run(workerCtx)
		close(workerDone)
	}()

	// Admin REST API — OIDC Bearer 토큰 + admin 그룹 필요
	apiAddr := env("MAIL_API_ADDR", ":8080")
	issuerURL := os.Getenv("MAIL_OIDC_ISSUER")
	devInsecure := os.Getenv("MAIL_DEV_INSECURE") == "true"
	// fail-closed: issuer가 없으면 기동 자체를 거부한다. env 하나 빠졌다고
	// admin API가 조용히 무인증 개방되는 사고 방지 — 검증 없는 dev 모드는
	// MAIL_DEV_INSECURE=true 명시 opt-in으로만.
	if issuerURL == "" && !devInsecure {
		log.Fatal("MAIL_OIDC_ISSUER 미설정 — 검증 없이 띄우려면 MAIL_DEV_INSECURE=true 명시 필요 (프로덕션 금지)")
	}
	if devInsecure {
		log.Printf("★★★ MAIL_DEV_INSECURE=true — API 토큰 검증 꺼짐, 전 요청 admin 취급 (프로덕션 금지) ★★★")
	}
	authCfg := api.AuthConfig{
		IssuerURL:          issuerURL,
		ClientID:           os.Getenv("MAIL_OIDC_CLIENT_ID"),
		AdminGroup:         env("MAIL_ADMIN_GROUP", "mail-admin"),
		InsecureSkipVerify: devInsecure,
	}
	authn, err := api.NewAuthenticator(context.Background(), authCfg)
	if err != nil {
		log.Fatalf("OIDC 초기화 실패: %v", err)
	}
	// 점검 대상 — smtps는 TLS 설정 시에만 존재
	systemPortList := []api.SystemPort{
		{Name: "imap", Addr: imapAddr, Kind: "imap", TLS: tlsConfig != nil, Check: true},
		{Name: "smtp", Addr: smtpAddr, Kind: "smtp", Check: true},
		{Name: "submission", Addr: submissionAddr, Kind: "smtp", Check: true},
		{Name: "smtps", Addr: smtpsAddr, Kind: "smtp", TLS: true, Check: tlsConfig != nil},
	}
	apiSrv := &http.Server{
		Addr: apiAddr,
		Handler: api.NewServer(st, authn).WithHostname(hostname).
			WithSystemPort(systemPortList).
			WithExternalPort(hostname, []api.ExternalPort{
				{Name: "imaps", Port: "993", Mode: "tls"},
				{Name: "smtp", Port: "25", Mode: "banner"},
				{Name: "submission", Port: "587", Mode: "banner"},
				{Name: "smtps", Port: "465", Mode: "tls"},
			}),
		// slowloris 방어 — 헤더/전체 읽기와 유휴 연결에 상한
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		log.Printf("maild: Admin API 시작 %s (issuer=%q group=%s)",
			apiAddr, authCfg.IssuerURL, authCfg.AdminGroup)
		errCh <- apiSrv.ListenAndServe()
	}()

	// ── graceful shutdown ────────────────────────────────────
	// SIGTERM(k8s)/SIGINT를 받으면: 새 연결 수락 중단 → 워커 정지 →
	// 유예 시간 내 정리. 서버 하나가 죽어도 전체 종료 (crash-fast).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	select {
	case sig := <-sigCh:
		log.Printf("maild: %s 수신 — graceful shutdown 시작", sig)
	case err := <-errCh:
		log.Printf("maild: 서버 오류 — shutdown: %v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	workerCancel() // 큐 워커: 현재 배치 마치고 정지
	_ = apiSrv.Shutdown(shutdownCtx)
	_ = smtpSrv.Shutdown(shutdownCtx)
	_ = subSrv.Shutdown(shutdownCtx)
	if smtpsSrv != nil {
		_ = smtpsSrv.Shutdown(shutdownCtx)
	}
	_ = imapSrv.Close()

	select {
	case <-workerDone:
	case <-shutdownCtx.Done():
		log.Printf("maild: 워커 종료 대기 타임아웃")
	}
	log.Printf("maild: 종료 완료")
}
