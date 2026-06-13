package window

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestResolve(t *testing.T) {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	cases := map[string]string{
		"":             "all",
		"all":          "all",
		"alltime":      "all",
		"daily":        "d=2026-06-13",
		"weekly":       "w=2026-W24",
		"monthly":      "m=2026-06",
		"s=spring2026": "s=spring2026", // literal passthrough
		"d=2025-01-01": "d=2025-01-01", // literal passthrough
	}
	for in, want := range cases {
		if got := Resolve(in, now); got != want {
			t.Errorf("Resolve(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseEnd(t *testing.T) {
	cases := []struct {
		id    string
		end   string // RFC3339 expected end (exclusive)
		dated bool
	}{
		{"d=2026-06-13", "2026-06-14T00:00:00Z", true},
		{"m=2026-06", "2026-07-01T00:00:00Z", true},
		{"w=2026-W24", "2026-06-15T00:00:00Z", true}, // W24 Mon 2026-06-08 .. next Mon 06-15
		{"all", "", false},
		{"s=spring2026", "", false},
		{"d=not-a-date", "", false},
	}
	for _, c := range cases {
		end, dated := ParseEnd(c.id)
		if dated != c.dated {
			t.Errorf("ParseEnd(%q) dated=%v, want %v", c.id, dated, c.dated)
			continue
		}
		if dated {
			want, _ := time.Parse(time.RFC3339, c.end)
			if !end.Equal(want) {
				t.Errorf("ParseEnd(%q) end=%v, want %v", c.id, end.Format(time.RFC3339), c.end)
			}
		}
	}
}

func TestReaperSweep(t *testing.T) {
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

	app := "reapertest"
	stale := "lb:{" + app + ":b:all:d=2020-01-01}:z" // long aged out
	fresh := "lb:{" + app + ":b:all:d=2999-01-01}:z" // far future
	allt := "lb:{" + app + ":b:all:all}:z"           // never expires
	for _, k := range []string{stale, fresh, allt} {
		rdb.ZAdd(ctx, k, redis.Z{Score: 1, Member: "m"})
	}
	t.Cleanup(func() { rdb.Del(ctx, stale, fresh, allt) })

	r := NewReaper(rdb, 24*time.Hour, time.Hour)
	n, err := r.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("expected at least the stale board expired, got %d", n)
	}
	// Stale key got a TTL; fresh and all-time did not.
	if ttl, _ := rdb.TTL(ctx, stale).Result(); ttl <= 0 {
		t.Errorf("stale key should have a TTL, got %v", ttl)
	}
	if ttl, _ := rdb.TTL(ctx, fresh).Result(); ttl > 0 {
		t.Errorf("fresh key should not be expired, got TTL %v", ttl)
	}
	if ttl, _ := rdb.TTL(ctx, allt).Result(); ttl > 0 {
		t.Errorf("all-time key should never expire, got TTL %v", ttl)
	}
}
