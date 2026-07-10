package postgres

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// b64 is used for app password hash encoding (standard base64 without padding).
var b64 = base64.RawStdEncoding

// subtleConstEq is a timing-attack-safe byte comparison.
func subtleConstEq(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

// HashPassword hashes a plaintext password with argon2id and returns the encoded string.
// Format: argon2id$<time>$<memoryKiB>$<threads>$<saltB64>$<hashB64>
// Used when issuing app passwords.
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("salt generation: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("argon2id$%d$%d$%d$%s$%s",
		argonTime, argonMemory, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(hash),
	), nil
}
