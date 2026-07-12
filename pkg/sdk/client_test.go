package sdk

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kodeni-am/leaderboard/pkg/api"
	"github.com/kodeni-am/leaderboard/pkg/engine"
	"github.com/kodeni-am/leaderboard/pkg/ingest"
	"github.com/kodeni-am/leaderboard/pkg/tenancy"
	"github.com/kodeni-am/leaderboard/pkg/users"
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
	srv := api.NewServer(eng, ing, store, registry, nil, false, users.NewMemStore())
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

func TestSDKUsers(t *testing.T) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewUniversalClient(&redis.UniversalOptions{Addrs: []string{addr}})
	pctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(pctx).Err(); err != nil {
		t.Skipf("redis not available: %v", err)
	}
	ctx := context.Background()

	eng := engine.NewRedisEngine(rdb)
	store := tenancy.NewMemStore()
	registry := ingest.NewStaticRegistry()
	log := ingest.NewMemLog()
	ing := ingest.NewIngestor(log, registry, ingest.NewMemDeduper())
	cons := ingest.NewConsumer(log, registry, eng)
	srv := api.NewServer(eng, ing, store, registry, nil, false, users.NewMemStore())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	app, key, err := store.CreateApp(ctx, "usr_sdk_test", "Racer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = eng.Reset(ctx, engine.Board{Key: engine.BoardKey{App: app.ID, Board: "high", Segment: "all", Window: "all"}})
	})
	c := New(ts.URL, key)
	if err := c.CreateBoard(ctx, BoardDef{Board: "high"}); err != nil {
		t.Fatal(err)
	}

	// Register + duplicate -> ErrNicknameTaken.
	u, err := c.RegisterUser(ctx, "Ninja")
	if err != nil || !strings.HasPrefix(u.UserID, "plr_") || u.Nickname != "Ninja" {
		t.Fatalf("RegisterUser: %+v / %v", u, err)
	}
	if _, err := c.RegisterUser(ctx, "ninja"); !errors.Is(err, ErrNicknameTaken) {
		t.Errorf("dup register: %v", err)
	}

	// Lookup both ways.
	if got, err := c.GetUser(ctx, u.UserID); err != nil || got.Nickname != "Ninja" {
		t.Fatalf("GetUser: %+v / %v", got, err)
	}
	if got, err := c.UserByNickname(ctx, "NINJA"); err != nil || got.UserID != u.UserID {
		t.Fatalf("UserByNickname: %+v / %v", got, err)
	}
	if _, err := c.GetUser(ctx, "plr_nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetUser unknown: %v", err)
	}

	// Rename.
	if ren, err := c.RenameUser(ctx, u.UserID, "Shadow"); err != nil || ren.Nickname != "Shadow" {
		t.Fatalf("RenameUser: %+v / %v", ren, err)
	}

	// Read enrichment: submit as the player, drain, and read the nickname back.
	if _, err := c.Submit(ctx, "high", Submission{Member: u.UserID, Score: 900}); err != nil {
		t.Fatal(err)
	}
	if err := cons.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	top, err := c.Top(ctx, "high", 10, QueryOpts{})
	if err != nil || len(top) != 1 || top[0].Nickname != "Shadow" {
		t.Fatalf("Top with nickname: %+v / %v", top, err)
	}

	// Claim an existing anonymous member id in place: submit raw first, then
	// register with Member set — the nickname attaches to the existing row.
	if _, err := c.Submit(ctx, "high", Submission{Member: "surfer-raw", Score: 300}); err != nil {
		t.Fatal(err)
	}
	if err := cons.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	cu, err := c.RegisterUser(ctx, "Kai", RegisterUserOpts{Member: "surfer-raw"})
	if err != nil || cu.UserID != "surfer-raw" || cu.Nickname != "Kai" {
		t.Fatalf("claim: %+v / %v", cu, err)
	}
	if _, err := c.RegisterUser(ctx, "Other", RegisterUserOpts{Member: "surfer-raw"}); !errors.Is(err, ErrMemberTaken) {
		t.Errorf("re-claim: got %v, want ErrMemberTaken", err)
	}
	top, err = c.Top(ctx, "high", 10, QueryOpts{})
	if err != nil || len(top) != 2 {
		t.Fatalf("Top after claim: %+v / %v", top, err)
	}
	if top[1].Member != "surfer-raw" || top[1].Nickname != "Kai" || top[1].Score != 300 {
		t.Fatalf("claimed row not enriched in place: %+v", top[1])
	}
}

func TestSDKModeration(t *testing.T) {
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
	srv := api.NewServer(eng, ing, store, registry, nil, false, users.NewMemStore())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	app, key, err := store.CreateApp(ctx, "usr_sdk_mod", "ModGame")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = eng.Reset(ctx, engine.Board{Key: engine.BoardKey{App: app.ID, Board: "high", Segment: "all", Window: "all"}})
	})

	c := New(ts.URL, key)
	if err := c.CreateBoard(ctx, BoardDef{Board: "high"}); err != nil {
		t.Fatal(err)
	}

	u, err := c.RegisterUser(ctx, "Ninja")
	if err != nil {
		t.Fatal(err)
	}
	for m, sc := range map[string]float64{u.UserID: 900, "raw-alice": 500} {
		if _, err := c.Submit(ctx, "high", Submission{Member: m, Score: sc}); err != nil {
			t.Fatal(err)
		}
	}
	if err := cons.Drain(ctx); err != nil {
		t.Fatal(err)
	}

	// RemoveScore: entry gone, member can still be re-submitted.
	if err := c.RemoveScore(ctx, "high", "raw-alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetRank(ctx, "high", "raw-alice", QueryOpts{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("raw-alice still ranked: %v", err)
	}
	// Idempotent.
	if err := c.RemoveScore(ctx, "high", "raw-alice"); err != nil {
		t.Fatalf("re-remove: %v", err)
	}

	// DeleteUser: scores gone, registration gone, nickname re-claimable.
	if err := c.DeleteUser(ctx, u.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetRank(ctx, "high", u.UserID, QueryOpts{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted player still ranked: %v", err)
	}
	if _, err := c.GetUser(ctx, u.UserID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted player still registered: %v", err)
	}
	if _, err := c.RegisterUser(ctx, "Ninja"); err != nil {
		t.Fatalf("nickname not released: %v", err)
	}
}
