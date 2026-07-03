// Command maild is the mail server daemon.
//
// Phase 1: IMAP 서버 + Postgres 저장 엔진.
// SMTP 수신/발송 큐는 Phase 2에서 조립한다.
package main

import (
	"context"
	"log"
	"os"

	"github.com/emersion/go-imap/v2/imapserver"

	imapbackend "github.com/krisamin/mail/internal/imap"
	"github.com/krisamin/mail/internal/store/postgres"
)

func main() {
	dsn := os.Getenv("MAIL_DSN")
	if dsn == "" {
		log.Fatal("MAIL_DSN 미설정 (예: postgres://mail:maildev@localhost:55432/mail)")
	}
	imapAddr := os.Getenv("MAIL_IMAP_ADDR")
	if imapAddr == "" {
		imapAddr = ":1143" // dev 기본값. 143은 권한 필요 → k8s에선 Service로 매핑
	}

	st, err := postgres.New(context.Background(), dsn)
	if err != nil {
		log.Fatalf("store 연결 실패: %v", err)
	}
	defer st.Close()

	backend := imapbackend.NewBackend(st)
	server := imapserver.New(&imapserver.Options{
		NewSession: backend.NewSession,
		// Phase 1 dev: TLS 없이 평문 LOGIN 허용. 프로덕션에선 TLSConfig 필수.
		InsecureAuth: true,
	})

	log.Printf("maild: IMAP 서버 시작 %s (InsecureAuth=dev)", imapAddr)
	if err := server.ListenAndServe(imapAddr); err != nil {
		log.Fatalf("IMAP 서버 종료: %v", err)
	}
}
