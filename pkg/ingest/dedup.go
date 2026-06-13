package ingest

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Deduper provides idempotency for submissions carrying a client dedup key.
// It matters most for UpdateIncrement boards, where a naive retry would
// double-count.
type Deduper interface {
	// SeenOrMark reports whether key was already recorded; if not, it marks it
	// (atomically) so a subsequent call returns true.
	SeenOrMark(ctx context.Context, key string, ttl time.Duration) (bool, error)
	// Unmark removes a key (used to roll back a reservation if the append fails).
	Unmark(ctx context.Context, key string) error
}

// RedisDeduper marks keys with SET NX EX.
type RedisDeduper struct {
	rdb    redis.UniversalClient
	prefix string
}

func NewRedisDeduper(rdb redis.UniversalClient) *RedisDeduper {
	return &RedisDeduper{rdb: rdb, prefix: "lb:idem:"}
}

func (d *RedisDeduper) SeenOrMark(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	ok, err := d.rdb.SetNX(ctx, d.prefix+key, 1, ttl).Result()
	if err != nil {
		return false, err
	}
	return !ok, nil // ok==true means we set it (i.e. not previously seen)
}

func (d *RedisDeduper) Unmark(ctx context.Context, key string) error {
	return d.rdb.Del(ctx, d.prefix+key).Err()
}

// MemDeduper is an in-memory Deduper for tests/local (ignores TTL).
type MemDeduper struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func NewMemDeduper() *MemDeduper { return &MemDeduper{seen: map[string]struct{}{}} }

func (d *MemDeduper) SeenOrMark(_ context.Context, key string, _ time.Duration) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[key]; ok {
		return true, nil
	}
	d.seen[key] = struct{}{}
	return false, nil
}

func (d *MemDeduper) Unmark(_ context.Context, key string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.seen, key)
	return nil
}

// NoopDeduper disables dedup (every submit is accepted).
type NoopDeduper struct{}

func (NoopDeduper) SeenOrMark(context.Context, string, time.Duration) (bool, error) {
	return false, nil
}
func (NoopDeduper) Unmark(context.Context, string) error { return nil }
