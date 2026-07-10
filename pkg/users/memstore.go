package users

import (
	"context"
	"sync"
	"time"
)

// MemStore is the in-memory Store for tests and single-process local runs.
type MemStore struct {
	mu    sync.Mutex
	users map[string]map[string]User   // app -> id -> user
	nicks map[string]map[string]string // app -> lower(nick) -> id
}

func NewMemStore() *MemStore {
	return &MemStore{
		users: map[string]map[string]User{},
		nicks: map[string]map[string]string{},
	}
}

func (m *MemStore) Create(_ context.Context, appID, nickname string) (User, error) {
	display, lower, err := normalizeNickname(nickname)
	if err != nil {
		return User{}, err
	}
	id, err := newID()
	if err != nil {
		return User{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, taken := m.nicks[appID][lower]; taken {
		return User{}, ErrNicknameTaken
	}
	now := time.Now().UTC()
	u := User{ID: id, Nickname: display, CreatedAt: now, UpdatedAt: now}
	if m.users[appID] == nil {
		m.users[appID] = map[string]User{}
		m.nicks[appID] = map[string]string{}
	}
	m.users[appID][id] = u
	m.nicks[appID][lower] = id
	return u, nil
}

func (m *MemStore) Get(_ context.Context, appID, id string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[appID][id]
	if !ok {
		return User{}, ErrNotFound
	}
	return u, nil
}

func (m *MemStore) GetByNickname(_ context.Context, appID, nickname string) (User, error) {
	_, lower, err := normalizeNickname(nickname)
	if err != nil {
		return User{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.nicks[appID][lower]
	if !ok {
		return User{}, ErrNotFound
	}
	return m.users[appID][id], nil
}

func (m *MemStore) Rename(_ context.Context, appID, id, nickname string) (User, error) {
	display, lower, err := normalizeNickname(nickname)
	if err != nil {
		return User{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[appID][id]
	if !ok {
		return User{}, ErrNotFound
	}
	_, oldLower, _ := normalizeNickname(u.Nickname)
	if lower != oldLower {
		if _, taken := m.nicks[appID][lower]; taken {
			return User{}, ErrNicknameTaken
		}
		delete(m.nicks[appID], oldLower)
		m.nicks[appID][lower] = id
	}
	u.Nickname = display
	u.UpdatedAt = time.Now().UTC()
	m.users[appID][id] = u
	return u, nil
}

func (m *MemStore) Nicknames(_ context.Context, appID string, ids []string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if u, ok := m.users[appID][id]; ok {
			out[id] = u.Nickname
		}
	}
	return out, nil
}

func (m *MemStore) Delete(_ context.Context, appID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[appID][id]
	if !ok {
		return nil
	}
	_, lower, err := normalizeNickname(u.Nickname)
	if err == nil && m.nicks[appID][lower] == id {
		delete(m.nicks[appID], lower)
	}
	delete(m.users[appID], id)
	return nil
}
