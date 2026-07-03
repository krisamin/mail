// Command maild is the mail server daemon.
//
// Phase 1: IMAP 서버 + Postgres 저장 엔진.
// Phase 2-1: SMTP 수신(MX) — 수신자 검증 + INBOX 배달.
package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/emersion/go-imap/v2/imapserver"
	gosmtp "github.com/emersion/go-smtp"

	imapbackend "github.com/krisamin/mail/internal/imap"
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
	// dev 기본값: 143/25는 특권 포트 → 1143/2525. k8s에선 Service로 매핑.
	imapAddr := env("MAIL_IMAP_ADDR", ":1143")
	smtpAddr := env("MAIL_SMTP_ADDR", ":2525")
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

	// SMTP 수신(MX) 서버
	smtpSrv := gosmtp.NewServer(smtpbackend.NewBackend(st, hostname))
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

	log.Fatalf("maild: 서버 종료: %v", <-errCh)
}
