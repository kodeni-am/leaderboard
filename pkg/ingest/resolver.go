package ingest

import (
	"errors"
	"sync"

	"github.com/araasr/leaderboard/pkg/engine"
)

// ErrUnknownBoard is returned when a submission targets a board that has not
// been registered.
var ErrUnknownBoard = errors.New("ingest: unknown board")

// BoardResolver maps an (app, board) pair to its logical definition, which the
// consumer needs to know the fan-out windows and score semantics. The SP5
// tenancy layer provides a persistent implementation; StaticRegistry serves
// tests and simple single-tenant deployments.
type BoardResolver interface {
	Resolve(app, board string) (engine.LogicalBoard, bool)
}

// StaticRegistry is an in-memory BoardResolver.
type StaticRegistry struct {
	mu sync.RWMutex
	m  map[string]engine.LogicalBoard
}

func NewStaticRegistry() *StaticRegistry {
	return &StaticRegistry{m: map[string]engine.LogicalBoard{}}
}

func regKey(app, board string) string { return app + "\x00" + board }

// Register adds or replaces a logical board definition.
func (r *StaticRegistry) Register(lb engine.LogicalBoard) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[regKey(lb.App, lb.Board)] = lb
}

func (r *StaticRegistry) Resolve(app, board string) (engine.LogicalBoard, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	lb, ok := r.m[regKey(app, board)]
	return lb, ok
}
