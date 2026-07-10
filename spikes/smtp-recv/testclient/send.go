// Spike-test SMTP client — throws one mail at the server.
// swaks substitute. Run: go run ./spikes/smtp-recv/testclient
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
		"Subject: shiro test mail",
		"Date: Wed, 01 Jul 2026 12:00:00 +0900",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"hello shiro!",
		"this is an SMTP spike test.",
		"let's see if it parses~",
	}, "\r\n")

	err := smtp.SendMail(
		"localhost:2525",
		nil, // no auth
		"me@example.com",
		[]string{"test@localhost"},
		[]byte(msg),
	)
	if err != nil {
		log.Fatalf("send failed: %v", err)
	}
	log.Println("✔ mail sent")
}
