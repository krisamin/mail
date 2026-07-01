// Command maild is the mail server daemon.
//
// 아직 골격만 있음. Phase 1부터 실제 SMTP/IMAP 백엔드 + 저장 엔진을
// 여기에 조립한다. 현재는 스파이크(spikes/smtp-recv)에서 프로토콜 흐름을
// 검증한 상태.
package main

import "log"

func main() {
	log.Println("maild: 아직 구현 전. 스파이크는 `go run ./spikes/smtp-recv` 참고.")
}
