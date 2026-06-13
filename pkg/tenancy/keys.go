package tenancy

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// randHex returns n random bytes hex-encoded.
func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// newID mints a prefixed random identifier (e.g. "app_3f9c...").
func newID(prefix string) (string, error) {
	h, err := randHex(12)
	if err != nil {
		return "", err
	}
	return prefix + h, nil
}

// newAPIKey returns a plaintext key (shown once) and its storage hash.
func newAPIKey() (plaintext, hash string, err error) {
	h, err := randHex(32)
	if err != nil {
		return "", "", err
	}
	plaintext = "lb_" + h
	return plaintext, hashKey(plaintext), nil
}

// hashKey is the deterministic hash used for storage and lookup.
func hashKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}
