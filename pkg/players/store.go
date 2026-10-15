// Package players proves who a leaderboard member is: a per-player token,
// bound to (app, member) on first use. Member ids are public (every board
// read returns them), so they cannot double as credentials; the token never
// leaves the player's install and the server keeps only its sha256.
package players

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

var (
	ErrTokenRequired = errors.New("players: player token required")
	ErrTokenMismatch = errors.New("players: player token mismatch")
	ErrInvalidToken  = errors.New("players: player token must be 32-128 characters of [0-9a-zA-Z_-]")
)

// Store binds and checks player tokens.
type Store interface {
	// Authorize binds token to (appID, member) if the member has no token yet
	// (atomic: concurrent first uses produce exactly one binding), otherwise
	// requires the same token. Returns ErrTokenRequired for "", ErrInvalidToken
	// for a malformed token, ErrTokenMismatch for a different one.
	Authorize(ctx context.Context, appID, member, token string) error
}

// ValidToken reports whether t is a well-formed player token.
func ValidToken(t string) bool {
	if len(t) < 32 || len(t) > 128 {
		return false
	}
	for _, r := range t {
		ok := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

func check(token string) error {
	if token == "" {
		return ErrTokenRequired
	}
	if !ValidToken(token) {
		return ErrInvalidToken
	}
	return nil
}
