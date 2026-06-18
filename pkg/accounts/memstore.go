package accounts

import (
	"context"
	"sync"
	"time"
)

// MemStores provides in-memory UserStore, SessionStore, and TokenStore for
// tests and single-process local runs.
type MemStores struct {
	mu       sync.Mutex
	users    map[string]User   // id -> user
	emailIdx map[string]string // email -> id
	sessions map[string]string // token -> userID
	userSess map[string]map[string]struct{}
	tokens   map[string]string // purpose|token -> userID
}

func NewMemStores() *MemStores {
	return &MemStores{
		users:    map[string]User{},
		emailIdx: map[string]string{},
		sessions: map[string]string{},
		userSess: map[string]map[string]struct{}{},
		tokens:   map[string]string{},
	}
}

func (m *MemStores) CreateUser(_ context.Context, u User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.emailIdx[u.Email]; ok {
		return ErrEmailTaken
	}
	m.users[u.ID] = u
	m.emailIdx[u.Email] = u.ID
	return nil
}

func (m *MemStores) GetByEmail(_ context.Context, email string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.emailIdx[email]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return m.users[id], nil
}

func (m *MemStores) GetByID(_ context.Context, id string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return u, nil
}

func (m *MemStores) Update(_ context.Context, u User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.users[u.ID]; !ok {
		return ErrUserNotFound
	}
	m.users[u.ID] = u
	return nil
}

func (m *MemStores) Create(_ context.Context, userID string, _ time.Duration) (string, error) {
	tok, err := newToken()
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[tok] = userID
	if m.userSess[userID] == nil {
		m.userSess[userID] = map[string]struct{}{}
	}
	m.userSess[userID][tok] = struct{}{}
	return tok, nil
}

func (m *MemStores) UserID(_ context.Context, token string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	uid, ok := m.sessions[token]
	if !ok {
		return "", ErrNoSession
	}
	return uid, nil
}

func (m *MemStores) Delete(_ context.Context, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if uid, ok := m.sessions[token]; ok {
		delete(m.sessions, token)
		delete(m.userSess[uid], token)
	}
	return nil
}

func (m *MemStores) DeleteAllForUser(_ context.Context, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for tok := range m.userSess[userID] {
		delete(m.sessions, tok)
	}
	delete(m.userSess, userID)
	return nil
}

func (m *MemStores) Issue(_ context.Context, purpose, userID string, _ time.Duration) (string, error) {
	tok, err := newToken()
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[purpose+"|"+tok] = userID
	return tok, nil
}

func (m *MemStores) Consume(_ context.Context, purpose, token string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := purpose + "|" + token
	uid, ok := m.tokens[key]
	if !ok {
		return "", ErrBadToken
	}
	delete(m.tokens, key)
	return uid, nil
}
