// Command maild is the mail server daemon.
//
// Phase 1: IMAP 서버 + Postgres 저장 엔진.
// Phase 2-1: SMTP 수신(MX) — 수신자 검증 + INBOX 배달.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/emersion/go-imap/v2/imapserver"
	gosmtp "github.com/emersion/go-smtp"

	"github.com/krisamin/mail/internal/api"
	imapbackend "github.com/krisamin/mail/internal/imap"
	"github.com/krisamin/mail/internal/queue"
	smtpbackend "github.com/krisamin/mail/internal/smtp"
	"github.com/krisamin/mail/internal/store/postgres"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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
	hostname := env("MAIL_HOSTNAME", "mail.krisam.in")

	st, err := postgres.New(context.Background(), dsn)
	if err != nil {
		log.Fatalf("store 연결 실패: %v", err)
	}
	defer st.Close()

	errCh := make(chan error, 2)

	// IMAP 서버
	imapSrv := imapserver.New(&imapserver.Options{
		NewSession: imapbackend.NewBackend(st).NewSession,
		// Phase 1 dev: TLS 없이 평문 LOGIN 허용. 프로덕션에선 TLSConfig 필수.
		InsecureAuth: true,
	})
	go func() {
		log.Printf("maild: IMAP 서버 시작 %s (InsecureAuth=dev)", imapAddr)
		errCh <- imapSrv.ListenAndServe(imapAddr)
	}()

	// SMTP 수신(MX) 서버 — SPF/DKIM/DMARC 검증 켬 (기록만, 거절 안 함)
	smtpSrv := gosmtp.NewServer(smtpbackend.NewBackend(st, hostname).WithInboundVerification())
	smtpSrv.Addr = smtpAddr
	smtpSrv.Domain = hostname
	smtpSrv.ReadTimeout = 60 * time.Second
	smtpSrv.WriteTimeout = 60 * time.Second
	smtpSrv.MaxMessageBytes = 25 * 1024 * 1024 // 25MB
	smtpSrv.MaxRecipients = 50
	go func() {
		log.Printf("maild: SMTP 수신 서버 시작 %s (hostname=%s)", smtpAddr, hostname)
		errCh <- smtpSrv.ListenAndServe()
	}()

	// SMTP submission 서버 (AUTH 필수 — 앱 비밀번호로 우리 유저가 제출)
	// 발송 relay는 DB에서 관리 (0005) — 외부 도메인 제출은 항상 큐로 받고,
	// 워커가 발송 시점에 relay를 해석한다 (도메인 지정 → default → env fallback).
	// relay가 하나도 없으면 큐에 쌓인 채 재시도되다가 어드민이 추가하면 나간다.
	subSrv := gosmtp.NewServer(smtpbackend.NewSubmissionBackend(st, hostname, true))
	subSrv.Addr = submissionAddr
	subSrv.Domain = hostname
	subSrv.ReadTimeout = 60 * time.Second
	subSrv.WriteTimeout = 60 * time.Second
	subSrv.MaxMessageBytes = 25 * 1024 * 1024
	subSrv.MaxRecipients = 50
	// Phase 2 dev: TLS 없이 평문 AUTH 허용. 프로덕션에선 TLSConfig 필수.
	subSrv.AllowInsecureAuth = true
	go func() {
		log.Printf("maild: SMTP submission 서버 시작 %s (AllowInsecureAuth=dev)", submissionAddr)
		errCh <- subSrv.ListenAndServe()
	}()

	// 발송 큐 워커 — relay는 DB 우선, env MAIL_RELAY_*는 fallback.
	var fallback *queue.RelayConfig
	if relayAddr := os.Getenv("MAIL_RELAY_ADDR"); relayAddr != "" {
		fallback = &queue.RelayConfig{
			Addr:     relayAddr,
			Username: os.Getenv("MAIL_RELAY_USERNAME"),
			Password: os.Getenv("MAIL_RELAY_PASSWORD"),
			StartTLS: os.Getenv("MAIL_RELAY_STARTTLS") != "false",
		}
		log.Printf("maild: env relay fallback 활성 (%s)", relayAddr)
	}
	worker := queue.NewWorker(st, queue.NewResolvingSender(st, fallback), queue.Config{}).
		WithSigner(queue.NewDKIMSigner(st))
	go worker.Run(context.Background())

	// Admin REST API (Phase 3) — OIDC Bearer 토큰 + admin 그룹 필요
	apiAddr := env("MAIL_API_ADDR", ":8080")
	authCfg := api.AuthConfig{
		IssuerURL:  os.Getenv("MAIL_OIDC_ISSUER"),
		ClientID:   os.Getenv("MAIL_OIDC_CLIENT_ID"),
		AdminGroup: env("MAIL_ADMIN_GROUP", "mail-admin"),
		// dev 전용: issuer 미설정이면 검증 없이 전부 admin 취급
		InsecureSkipVerify: os.Getenv("MAIL_OIDC_ISSUER") == "",
	}
	authn, err := api.NewAuthenticator(context.Background(), authCfg)
	if err != nil {
		log.Fatalf("OIDC 초기화 실패: %v", err)
	}
	apiSrv := &http.Server{Addr: apiAddr, Handler: api.NewServer(st, authn).WithHostname(hostname)}
	go func() {
		log.Printf("maild: Admin API 시작 %s (issuer=%q group=%s)",
			apiAddr, authCfg.IssuerURL, authCfg.AdminGroup)
		errCh <- apiSrv.ListenAndServe()
	}()

	log.Fatalf("maild: 서버 종료: %v", <-errCh)
}
