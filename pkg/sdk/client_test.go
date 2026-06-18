package sdk

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/kodeni-am/leaderboard/pkg/api"
	"github.com/kodeni-am/leaderboard/pkg/engine"
	"github.com/kodeni-am/leaderboard/pkg/ingest"
	"github.com/kodeni-am/leaderboard/pkg/tenancy"
	"github.com/redis/go-redis/v9"
)

func TestSDKAgainstServer(t *testing.T) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewUniversalClient(&redis.UniversalOptions{Addrs: []string{addr}})
	ctx := context.Background()
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := rdb.Ping(pctx).Err(); err != nil {
		t.Skipf("redis not available: %v", err)
	}

	eng := engine.NewRedisEngine(rdb)
	store := tenancy.NewMemStore()
	registry := ingest.NewStaticRegistry()
	log := ingest.NewMemLog()
	ing := ingest.NewIngestor(log, registry, ingest.NewMemDeduper())
	cons := ingest.NewConsumer(log, registry, eng)
	srv := api.NewServer(eng, ing, store, registry, nil, false)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Provision a tenant directly (this SDK test exercises the API-key data
	// plane, not the human account flow).
	app, key, err := store.CreateApp(ctx, "usr_sdk_test", "Racer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = eng.Reset(ctx, engine.Board{Key: engine.BoardKey{App: app.ID, Board: "laptimes", Segment: "all", Window: "all"}})
	})

	c := New(ts.URL, key)

	// Define a "lower is better" race board.
	if err := c.CreateBoard(ctx, BoardDef{Board: "laptimes", SortOrder: "asc", UpdatePolicy: "best"}); err != nil {
		t.Fatal(err)
	}

	// Submit lap times (write-behind).
	for _, s := range []struct {
		m string
		v float64
	}{{"mario", 90.5}, {"luigi", 88.2}, {"peach", 95.0}} {
		acc, err := c.Submit(ctx, "laptimes", Submission{Member: s.m, Score: s.v})
		if err != nil || !acc {
			t.Fatalf("submit %s: acc=%v err=%v", s.m, acc, err)
		}
	}
	if err := cons.Drain(ctx); err != nil {
		t.Fatal(err)
	}

	// luigi (88.2) is fastest -> rank 1 on an ascending board.
	e, err := c.GetRank(ctx, "laptimes", "luigi", QueryOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if e.Rank != 1 || e.Score != 88.2 {
		t.Errorf("luigi rank=%d score=%v, want 1/88.2", e.Rank, e.Score)
	}

	// Missing member -> ErrNotFound.
	if _, err := c.GetRank(ctx, "laptimes", "bowser", QueryOpts{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing member: got %v, want ErrNotFound", err)
	}

	top, err := c.Top(ctx, "laptimes", 3, QueryOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 3 || top[0].Member != "luigi" || top[2].Member != "peach" {
		t.Errorf("top = %+v", top)
	}

	nb, err := c.Neighbors(ctx, "laptimes", "mario", 1, QueryOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(nb) != 3 || nb[1].Member != "mario" {
		t.Errorf("neighbors = %+v", nb)
	}

	fr, err := c.Friends(ctx, "laptimes", []string{"peach", "luigi"}, QueryOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(fr) != 2 || fr[0].Member != "luigi" {
		t.Errorf("friends = %+v", fr)
	}
}
