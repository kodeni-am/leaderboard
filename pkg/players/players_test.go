package players

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

const tokA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const tokB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func contract(t *testing.T, s Store, app string) {
	ctx := context.Background()
	if err := s.Authorize(ctx, app, "m1", ""); !errors.Is(err, ErrTokenRequired) {
		t.Fatalf("empty token: %v", err)
	}
	if err := s.Authorize(ctx, app, "m1", "short"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("short token: %v", err)
	}
	if err := s.Authorize(ctx, app, "m1", strings.Repeat("!", 40)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("bad charset: %v", err)
	}
	if err := s.Authorize(ctx, app, "m1", tokA); err != nil {
		t.Fatalf("first use binds: %v", err)
	}
	if err := s.Authorize(ctx, app, "m1", tokA); err != nil {
		t.Fatalf("same token again: %v", err)
	}
	if err := s.Authorize(ctx, app, "m1", tokB); !errors.Is(err, ErrTokenMismatch) {
		t.Fatalf("other token: %v", err)
	}
	// bindings are per member and per app
	if err := s.Authorize(ctx, app, "m2", tokB); err != nil {
		t.Fatalf("other member binds independently: %v", err)
	}
	if err := s.Authorize(ctx, app+"x", "m1", tokB); err != nil {
		t.Fatalf("other app binds independently: %v", err)
	}
	// concurrent first use: exactly one winner
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i, tok := range []string{tokA, tokB, tokA + "c", tokB + "d"} {
		wg.Add(1)
		go func(i int, tok string) {
			defer wg.Done()
			if s.Authorize(ctx, app, "race", tok) == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(i, tok)
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("race: %d winners, want 1", wins)
	}
	// bindings counts distinct members bound per app: m1, m2, race
	if n, err := s.Bindings(ctx, app); err != nil || n != 3 {
		t.Fatalf("bindings(%s) = %d, %v; want 3", app, n, err)
	}
	if n, err := s.Bindings(ctx, app+"x"); err != nil || n != 1 {
		t.Fatalf("bindings(%sx) = %d, %v; want 1", app, n, err)
	}
}

func TestMemStore(t *testing.T) { contract(t, NewMemStore(), "app_mem") }

func TestRedisStore(t *testing.T) {
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
	app := "app_test_" + time.Now().Format("150405.000000")
	t.Cleanup(func() {
		for _, pat := range []string{"ply:tok:" + app + "*", "ply:count:" + app + "*"} {
			keys, _ := rdb.Keys(context.Background(), pat).Result()
			if len(keys) > 0 {
				rdb.Del(context.Background(), keys...)
			}
		}
	})
	contract(t, NewRedisStore(rdb), app)
}
