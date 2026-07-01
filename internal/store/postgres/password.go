package postgres

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// b64는 앱 비밀번호 해시 인코딩용 (패딩 없는 표준 base64).
var b64 = base64.RawStdEncoding

// subtleConstEq는 타이밍 공격에 안전한 바이트 비교.
func subtleConstEq(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

// HashPassword는 평문 비밀번호를 argon2id로 해싱해 인코딩 문자열을 반환한다.
// 포맷: argon2id$<time>$<memoryKiB>$<threads>$<saltB64>$<hashB64>
// 앱 비밀번호 발급 시 사용.
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("salt 생성: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("argon2id$%d$%d$%d$%s$%s",
		argonTime, argonMemory, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(hash),
	), nil
}
