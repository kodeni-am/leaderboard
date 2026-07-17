// Package users is the per-app player registry: player IDs — server-minted
// (plr_...) or caller-claimed existing board member ids — with friendly
// nicknames that are unique per app, case-insensitively. It is separate from
// accounts (dashboard humans) — a users.User is a player inside a game. The
// player ID is the string games submit as the leaderboard member, so renames
// never touch board data, and claiming an anonymous member id attaches a
// nickname to its existing rows in place.
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
	ErrInvalidNickname = errors.New("users: nickname must be 1-32 characters with no control or format characters")
	// ErrMemberTaken is returned when a caller-supplied member id is already a
	// registered player. Distinct from ErrNicknameTaken so clients can tell
	// "pick another name" apart from "this member is already claimed".
	ErrMemberTaken   = errors.New("users: member id already registered")
	ErrInvalidMember = errors.New("users: member id must be 1-64 characters with no control or format characters and must not start with plr_")
	// ErrRenameContention is returned when a rename kept losing to concurrent
	// renames of the same player and exhausted its retries.
	ErrRenameContention = errors.New("users: rename contention, retry")
	// ErrDeleteContention is returned when a delete kept losing to concurrent
	// renames of the same player and exhausted its retries.
	ErrDeleteContention = errors.New("users: delete contention, retry")
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
	// Create registers a player and claims nickname. With member == "" it
	// mints a plr_ id; a non-empty member claims that existing (anonymous)
	// board member id in place, so the nickname attaches to the member's
	// existing rows. Returns ErrNicknameTaken, ErrInvalidNickname,
	// ErrMemberTaken (member already registered), or ErrInvalidMember.
	Create(ctx context.Context, appID, nickname, member string) (User, error)
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
	// Delete removes a player's registration and releases the nickname for
	// re-use. Unknown ids are a no-op (nil) so deletion is idempotent. The
	// nickname claim is released only if it still maps to this id — player ids
	// are never reused, so a replayed delete can never affect a later
	// registration that re-claimed the name.
	Delete(ctx context.Context, appID, id string) error
	// Count returns the number of registered players in the app. An unknown
	// app counts zero — this is not an authorization boundary; callers that
	// need one check ownership before calling.
	Count(ctx context.Context, appID string) (int64, error)
}

// normalizeNickname trims and validates nick, returning the display form and
// the lowercased uniqueness key.
func normalizeNickname(nick string) (display, lower string, err error) {
	display = strings.TrimSpace(nick)
	if display == "" || utf8.RuneCountInString(display) > 32 {
		return "", "", ErrInvalidNickname
	}
	for _, r := range display {
		if unicode.Is(unicode.C, r) { // control, format, surrogate, private use
			return "", "", ErrInvalidNickname
		}
	}
	return display, strings.ToLower(display), nil
}

// normalizeMemberID trims and validates a caller-supplied member id being
// claimed. The plr_ prefix is reserved for the server-minted namespace — a
// client must never be able to occupy it. The 64-rune cap bounds what boards
// accept as member strings from this path.
func normalizeMemberID(member string) (string, error) {
	member = strings.TrimSpace(member)
	if member == "" || utf8.RuneCountInString(member) > 64 || strings.HasPrefix(member, "plr_") {
		return "", ErrInvalidMember
	}
	for _, r := range member {
		if unicode.Is(unicode.C, r) { // control, format, surrogate, private use
			return "", ErrInvalidMember
		}
	}
	return member, nil
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
