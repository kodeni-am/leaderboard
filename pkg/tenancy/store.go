// Package tenancy is the SP5 multi-tenant control plane: apps (tenants),
// hashed API keys, and per-app board definitions. It has no billing — it exists
// to isolate tenants and to resolve which logical board a request targets.
package tenancy

import (
	"context"
	"errors"
	"time"

	"github.com/araasr/leaderboard/pkg/engine"
)

var (
	ErrAppNotFound   = errors.New("tenancy: app not found")
	ErrBoardNotFound = errors.New("tenancy: board not found")
	ErrInvalidKey    = errors.New("tenancy: invalid api key")
)

// App is a tenant. The plaintext API key is shown once at creation and never
// stored — only its hash is persisted.
type App struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Store persists tenants and their board definitions.
type Store interface {
	// CreateApp registers a new tenant and returns it plus the one-time
	// plaintext API key.
	CreateApp(ctx context.Context, name string) (App, string, error)
	// GetApp looks up a tenant by id.
	GetApp(ctx context.Context, id string) (App, error)
	// AppByKey authenticates a plaintext API key to its owning tenant.
	AppByKey(ctx context.Context, plaintextKey string) (App, error)

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
