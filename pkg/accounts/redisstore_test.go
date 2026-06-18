package accounts

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func testRedisStores(t *testing.T) *RedisStores {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewUniversalClient(&redis.UniversalOptions{Addrs: []string{addr}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("redis not available: %v", err)
	}
	return NewRedisStores(rdb)
}

func TestRedisStoresFlow(t *testing.T) {
	ctx := context.Background()
	s := testRedisStores(t)
	id, _ := newID("usr_")
	u := User{ID: id, Email: id + "@example.com", PasswordHash: "x", CreatedAt: time.Now().UTC()}

	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, u); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("duplicate email: %v", err)
	}
	got, err := s.GetByEmail(ctx, u.Email)
	if err != nil || got.ID != u.ID {
		t.Fatalf("GetByEmail: %v / %v", got, err)
	}
	// The password hash MUST survive serialization (regression guard).
	if got.PasswordHash != u.PasswordHash {
		t.Fatalf("password hash not persisted: got %q want %q", got.PasswordHash, u.PasswordHash)
	}

	// Sessions: create, resolve, revoke-all.
	tok, err := s.Create(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if uid, err := s.UserID(ctx, tok); err != nil || uid != u.ID {
		t.Fatalf("UserID: %v / %v", uid, err)
	}
	if err := s.DeleteAllForUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserID(ctx, tok); !errors.Is(err, ErrNoSession) {
		t.Errorf("session should be revoked, got %v", err)
	}

	// Tokens: one-time consume.
	vt, _ := s.Issue(ctx, "verify", u.ID, time.Hour)
	if uid, err := s.Consume(ctx, "verify", vt); err != nil || uid != u.ID {
		t.Fatalf("Consume: %v / %v", uid, err)
	}
	if _, err := s.Consume(ctx, "verify", vt); !errors.Is(err, ErrBadToken) {
		t.Errorf("token should be single-use, got %v", err)
	}
}
