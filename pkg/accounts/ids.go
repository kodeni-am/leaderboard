package accounts

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func newID(prefix string) (string, error) {
	h, err := randHex(12)
	if err != nil {
		return "", err
	}
	return prefix + h, nil
}

// newToken returns a 256-bit random URL-safe token (hex).
func newToken() (string, error) { return randHex(32) }

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
