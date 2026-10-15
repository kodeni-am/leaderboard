package players

import (
	"context"
	"crypto/subtle"
	"sync"
)

// MemStore is an in-memory Store for tests and single-process runs.
type MemStore struct {
	mu sync.Mutex
	m  map[string]string // app + "\x00" + member -> token hash
}

func NewMemStore() *MemStore { return &MemStore{m: map[string]string{}} }

func (s *MemStore) Authorize(_ context.Context, appID, member, token string) error {
	if err := check(token); err != nil {
		return err
	}
	h := hashToken(token)
	k := appID + "\x00" + member
	s.mu.Lock()
	defer s.mu.Unlock()
	bound, ok := s.m[k]
	if !ok {
		s.m[k] = h
		return nil
	}
	if subtle.ConstantTimeCompare([]byte(bound), []byte(h)) != 1 {
		return ErrTokenMismatch
	}
	return nil
}
