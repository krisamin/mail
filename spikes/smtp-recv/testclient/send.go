// 스파이크 테스트용 SMTP 클라이언트 — 서버에 메일 한 통 던진다.
// swaks 대용. 실행: go run ./spikes/smtp-recv/testclient
package main

import (
	"log"
	"net/smtp"
	"strings"
)

func main() {
	msg := strings.Join([]string{
		"From: Maro <me@example.com>",
		"To: Test <test@localhost>",
		"Subject: 시로 테스트 메일",
		"Date: Wed, 01 Jul 2026 12:00:00 +0900",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"안녕 시로!",
		"이건 SMTP 스파이크 테스트야.",
		"잘 파싱되나 보자~",
	}, "\r\n")

	err := smtp.SendMail(
		"localhost:2525",
		nil, // 인증 없음
		"me@example.com",
		[]string{"test@localhost"},
		[]byte(msg),
	)
	if err != nil {
		log.Fatalf("전송 실패: %v", err)
	}
	log.Println("✔ 메일 전송 완료")
}
