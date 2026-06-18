// Package tenancy is the SP5 multi-tenant control plane: apps (tenants),
// hashed API keys, and per-app board definitions. It has no billing — it exists
// to isolate tenants and to resolve which logical board a request targets.
package tenancy

import (
	"context"
	"errors"
	"time"

	"github.com/kodeni-am/leaderboard/pkg/engine"
)

var (
	ErrAppNotFound   = errors.New("tenancy: app not found")
	ErrBoardNotFound = errors.New("tenancy: board not found")
	ErrInvalidKey    = errors.New("tenancy: invalid api key")
	ErrKeyNotFound   = errors.New("tenancy: api key not found")
)

// APIKey is the non-secret metadata for an issued key. The secret itself is
// only ever returned once (at issue time); only a hash is stored. Prefix is a
// masked identifier for display, e.g. "lb_3f9c…a1b2".
type APIKey struct {
	ID        string    `json:"id"`
	AppID     string    `json:"app_id"`
	Prefix    string    `json:"prefix"`
	CreatedAt time.Time `json:"created_at"`
}

// App is a tenant, owned by a dashboard user. The plaintext API key is shown
// once at creation and never stored — only its hash is persisted.
type App struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	OwnerUserID string    `json:"owner_user_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	// RequireSigning makes the API reject unsigned score submissions for this
	// app (HMAC anti-cheat). Opt-in per app, so apps that don't enable it keep
	// API-key-only auth. The signing secret is derived from the server master
	// key — see trust.DeriveAppSecret — never stored here.
	RequireSigning bool `json:"require_signing,omitempty"`
	// SigningKeyVersion rotates the derived signing secret: bumping it changes
	// the secret and invalidates signatures made with the prior one. Starts at 1.
	SigningKeyVersion int `json:"signing_key_version,omitempty"`
}

// Store persists tenants and their board definitions.
type Store interface {
	// CreateApp registers a new tenant owned by ownerUserID and returns it plus
	// the one-time plaintext API key.
	CreateApp(ctx context.Context, ownerUserID, name string) (App, string, error)
	// GetApp looks up a tenant by id.
	GetApp(ctx context.Context, id string) (App, error)
	// ListApps returns all apps owned by a user.
	ListApps(ctx context.Context, ownerUserID string) ([]App, error)
	// AppByKey authenticates a plaintext API key to its owning tenant.
	AppByKey(ctx context.Context, plaintextKey string) (App, error)

	// IssueKey mints an additional API key for an app (zero-downtime rotation:
	// add a new key, migrate clients, then revoke the old). Returns the
	// one-time plaintext key plus its metadata.
	IssueKey(ctx context.Context, appID string) (plaintext string, key APIKey, err error)
	// ListKeys returns the non-secret metadata for an app's keys.
	ListKeys(ctx context.Context, appID string) ([]APIKey, error)
	// RevokeKey invalidates a single key by id (must belong to appID).
	RevokeKey(ctx context.Context, appID, keyID string) error
	// DeleteApp removes an app and all of its keys and boards.
	DeleteApp(ctx context.Context, appID string) error

	// SetRequireSigning toggles whether the app rejects unsigned submissions,
	// returning the updated app.
	SetRequireSigning(ctx context.Context, appID string, require bool) (App, error)
	// RotateSigningKey bumps the app's signing key version (rotating its derived
	// secret) and returns the updated app.
	RotateSigningKey(ctx context.Context, appID string) (App, error)

	// UpsertBoard stores a board definition under its app.
	UpsertBoard(ctx context.Context, lb engine.LogicalBoard) error
	// GetBoard fetches one board definition.
	GetBoard(ctx context.Context, app, board string) (engine.LogicalBoard, error)
	// ListBoards returns all of an app's board definitions.
	ListBoards(ctx context.Context, app string) ([]engine.LogicalBoard, error)
	// AllBoards returns every board across all apps (used to warm the in-memory
	// resolver at startup).
	AllBoards(ctx context.Context) ([]engine.LogicalBoard, error)
}
