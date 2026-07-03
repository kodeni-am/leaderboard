// Package users is the per-app player registry: server-minted player IDs
// (plr_...) with friendly nicknames that are unique per app,
// case-insensitively. It is separate from accounts (dashboard humans) — a
// users.User is a player inside a game. The player ID is the string games
// submit as the leaderboard member, so renames never touch board data.
package users

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrNotFound        = errors.New("users: user not found")
	ErrNicknameTaken   = errors.New("users: nickname already taken")
	ErrInvalidNickname = errors.New("users: nickname must be 1-32 characters with no control characters")
	// ErrRenameContention is returned when a rename kept losing to concurrent
	// renames of the same player and exhausted its retries.
	ErrRenameContention = errors.New("users: rename contention, retry")
)

// User is a registered player within one app.
type User struct {
	ID        string    `json:"user_id"`
	Nickname  string    `json:"nickname"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Store persists players per app. Nickname uniqueness is enforced on the
// lowercased form; the display form is stored as entered.
type Store interface {
	// Create mints a plr_ id and claims nickname.
	// Returns ErrNicknameTaken or ErrInvalidNickname.
	Create(ctx context.Context, appID, nickname string) (User, error)
	// Get returns the player by id, or ErrNotFound.
	Get(ctx context.Context, appID, id string) (User, error)
	// GetByNickname resolves a nickname case-insensitively, or ErrNotFound.
	GetByNickname(ctx context.Context, appID, nickname string) (User, error)
	// Rename atomically claims the new nickname and releases the old one.
	// Implementations may return ErrRenameContention if concurrent renames of the same player exhaust retries.
	Rename(ctx context.Context, appID, id, nickname string) (User, error)
	// Nicknames returns id -> display nickname for the ids that are
	// registered players; unregistered ids are simply absent from the map.
	Nicknames(ctx context.Context, appID string, ids []string) (map[string]string, error)
}

// normalizeNickname trims and validates nick, returning the display form and
// the lowercased uniqueness key.
func normalizeNickname(nick string) (display, lower string, err error) {
	display = strings.TrimSpace(nick)
	if display == "" || utf8.RuneCountInString(display) > 32 {
		return "", "", ErrInvalidNickname
	}
	for _, r := range display {
		if unicode.IsControl(r) {
			return "", "", ErrInvalidNickname
		}
	}
	return display, strings.ToLower(display), nil
}

// newID mints a player id ("plr_" + 12 random hex bytes). The prefix differs
// from accounts' usr_ so the two identity types are distinguishable.
func newID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "plr_" + hex.EncodeToString(b), nil
}
