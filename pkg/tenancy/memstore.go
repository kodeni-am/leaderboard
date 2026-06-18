package tenancy

import (
	"context"
	"sync"
	"time"

	"github.com/kodeni-am/leaderboard/pkg/engine"
)

// MemStore is an in-memory Store for tests and single-process local runs.
type MemStore struct {
	mu       sync.RWMutex
	apps     map[string]App                            // id -> app
	keyIndex map[string]string                         // keyHash -> appID
	boards   map[string]map[string]engine.LogicalBoard // appID -> board -> def
	now      func() time.Time
}

func NewMemStore() *MemStore {
	return &MemStore{
		apps:     map[string]App{},
		keyIndex: map[string]string{},
		boards:   map[string]map[string]engine.LogicalBoard{},
		now:      func() time.Time { return time.Now().UTC() },
	}
}

func (s *MemStore) CreateApp(_ context.Context, name string) (App, string, error) {
	id, err := newID("app_")
	if err != nil {
		return App{}, "", err
	}
	plain, hash, err := newAPIKey()
	if err != nil {
		return App{}, "", err
	}
	app := App{ID: id, Name: name, CreatedAt: s.now()}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apps[id] = app
	s.keyIndex[hash] = id
	s.boards[id] = map[string]engine.LogicalBoard{}
	return app, plain, nil
}

func (s *MemStore) GetApp(_ context.Context, id string) (App, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	app, ok := s.apps[id]
	if !ok {
		return App{}, ErrAppNotFound
	}
	return app, nil
}

func (s *MemStore) AppByKey(_ context.Context, plaintextKey string) (App, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.keyIndex[hashKey(plaintextKey)]
	if !ok {
		return App{}, ErrInvalidKey
	}
	return s.apps[id], nil
}

func (s *MemStore) UpsertBoard(_ context.Context, lb engine.LogicalBoard) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[lb.App]; !ok {
		return ErrAppNotFound
	}
	if s.boards[lb.App] == nil {
		s.boards[lb.App] = map[string]engine.LogicalBoard{}
	}
	s.boards[lb.App][lb.Board] = lb
	return nil
}

func (s *MemStore) GetBoard(_ context.Context, app, board string) (engine.LogicalBoard, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	lb, ok := s.boards[app][board]
	if !ok {
		return engine.LogicalBoard{}, ErrBoardNotFound
	}
	return lb, nil
}

func (s *MemStore) ListBoards(_ context.Context, app string) ([]engine.LogicalBoard, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]engine.LogicalBoard, 0, len(s.boards[app]))
	for _, lb := range s.boards[app] {
		out = append(out, lb)
	}
	return out, nil
}

func (s *MemStore) AllBoards(_ context.Context) ([]engine.LogicalBoard, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []engine.LogicalBoard
	for _, byBoard := range s.boards {
		for _, lb := range byBoard {
			out = append(out, lb)
		}
	}
	return out, nil
}
