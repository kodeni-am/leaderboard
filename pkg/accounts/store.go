// Package accounts is the human authentication subsystem (SP9): user accounts,
// passwords, sessions, and one-time email tokens. It is separate from the
// API-key data plane — sessions authenticate people in the dashboard; API keys
// authenticate game clients.
package accounts

import (
	"context"
	"errors"
	"time"
)

var (
	ErrEmailTaken         = errors.New("accounts: email already registered")
	ErrUserNotFound       = errors.New("accounts: user not found")
	ErrNoSession          = errors.New("accounts: no such session")
	ErrBadToken           = errors.New("accounts: invalid or expired token")
	ErrInvalidCredentials = errors.New("accounts: invalid email or password")
	ErrEmailNotVerified   = errors.New("accounts: email not verified")
	ErrWeakPassword       = errors.New("accounts: password must be at least 8 characters")
	ErrInvalidEmail       = errors.New("accounts: invalid email address")
)

// User is a dashboard account. PasswordHash is a bcrypt hash.
//
// NOTE: PasswordHash IS serialized (the Redis store persists the struct as
// JSON), so it must not be tagged json:"-". API responses never marshal User
// directly — the handlers build explicit response maps — so the hash is not
// exposed over HTTP.
type User struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	PasswordHash  string    `json:"password_hash"`
	EmailVerified bool      `json:"email_verified"`
	CreatedAt     time.Time `json:"created_at"`
}

// UserStore persists user accounts.
type UserStore interface {
	// CreateUser stores u; returns ErrEmailTaken if the email already exists.
	CreateUser(ctx context.Context, u User) error
	GetByEmail(ctx context.Context, email string) (User, error)
	GetByID(ctx context.Context, id string) (User, error)
	Update(ctx context.Context, u User) error
}

// SessionStore manages login sessions (opaque tokens -> user id).
type SessionStore interface {
	Create(ctx context.Context, userID string, ttl time.Duration) (token string, err error)
	UserID(ctx context.Context, token string) (string, error) // ErrNoSession
	Delete(ctx context.Context, token string) error
	DeleteAllForUser(ctx context.Context, userID string) error
}

// TokenStore manages one-time, TTL'd tokens for email verification and password
// reset. Consume is single-use.
type TokenStore interface {
	Issue(ctx context.Context, purpose, userID string, ttl time.Duration) (token string, err error)
	Consume(ctx context.Context, purpose, token string) (userID string, err error) // ErrBadToken
}
