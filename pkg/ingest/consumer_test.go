package ingest

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/araasr/leaderboard/pkg/engine"
	"github.com/redis/go-redis/v9"
)

func testRedis(t *testing.T) redis.UniversalClient {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewUniversalClient(&redis.UniversalOptions{Addrs: []string{addr}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("redis not available at %s: %v", addr, err)
	}
	return rdb
}

func ns(t *testing.T) string {
	return strings.NewReplacer("/", "-", " ", "_").Replace(t.Name())
}

func TestConsumerAppliesAndFansOut(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	eng := engine.NewRedisEngine(rdb)
	app := ns(t)

	reg := NewStaticRegistry()
	lb := engine.LogicalBoard{
		App:     app,
		Board:   "score",
		Windows: []engine.WindowSpec{{Kind: engine.WindowAllTime}, {Kind: engine.WindowDaily}},
	}
	reg.Register(lb)

	log := NewMemLog()
	ing := NewIngestor(log, reg, NewMemDeduper())
	now := time.Now().UTC()

	subs := []struct {
		member string
		score  float64
	}{{"alice", 300}, {"bob", 500}, {"carol", 100}}
	for _, s := range subs {
		acc, err := ing.Submit(ctx, Record{
			App: app, Board: "score", Member: s.member, Score: s.score,
			Time: now, Segments: []string{"all", "region=eu"},
		})
		if err != nil || !acc {
			t.Fatalf("submit %s: acc=%v err=%v", s.member, acc, err)
		}
	}

	// Boards aren't ranked yet — nothing consumed.
	allTime := engine.Board{Key: engine.BoardKey{App: app, Board: "score", Segment: "all", Window: "all"}}
	t.Cleanup(func() { resetFanout(ctx, eng, app, now) })
	if c, _ := eng.Count(ctx, allTime); c != 0 {
		t.Fatalf("engine should be empty before consume, got %d", c)
	}

	// Drain the log into the engine.
	cons := NewConsumer(log, reg, eng)
	if err := cons.Drain(ctx); err != nil {
		t.Fatal(err)
	}

	// Verify ranking on the all-time/all-segment board.
	re, err := eng.GetRank(ctx, allTime, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if re.Rank != 1 || re.Score != 500 {
		t.Errorf("bob: rank=%d score=%v, want rank 1 score 500", re.Rank, re.Score)
	}
	// Fan-out reached the daily and segmented boards too.
	daily := engine.Board{Key: engine.BoardKey{App: app, Board: "score", Segment: "all", Window: (engine.WindowSpec{Kind: engine.WindowDaily}).WindowID(now)}}
	seg := engine.Board{Key: engine.BoardKey{App: app, Board: "score", Segment: "region=eu", Window: "all"}}
	for name, b := range map[string]engine.Board{"daily": daily, "segment": seg} {
		if c, _ := eng.Count(ctx, b); c != 3 {
			t.Errorf("%s board count = %d, want 3", name, c)
		}
	}
}

func resetFanout(ctx context.Context, eng *engine.RedisEngine, app string, now time.Time) {
	lb := engine.LogicalBoard{App: app, Board: "score", Windows: []engine.WindowSpec{{Kind: engine.WindowAllTime}, {Kind: engine.WindowDaily}}}
	for _, k := range engine.DerivePhysicalBoards(lb, engine.Event{Time: now, Segments: []string{"all", "region=eu"}}) {
		_ = eng.Reset(ctx, engine.Board{Key: k})
	}
}

func TestConsumerIncrementalCursor(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	eng := engine.NewRedisEngine(rdb)
	app := ns(t)
	reg := NewStaticRegistry()
	reg.Register(engine.LogicalBoard{App: app, Board: "score"})
	board := engine.Board{Key: engine.BoardKey{App: app, Board: "score", Segment: "all", Window: "all"}}
	_ = eng.Reset(ctx, board)
	t.Cleanup(func() { _ = eng.Reset(ctx, board) })

	log := NewMemLog()
	ing := NewIngestor(log, reg, NewMemDeduper())
	cons := NewConsumer(log, reg, eng)

	ing.Submit(ctx, Record{App: app, Board: "score", Member: "p1", Score: 10})
	n, _ := cons.Step(ctx)
	if n != 1 {
		t.Fatalf("first step processed %d, want 1", n)
	}
	// A second step with no new records advances nothing.
	if n, _ := cons.Step(ctx); n != 0 {
		t.Fatalf("idle step processed %d, want 0", n)
	}
	// New record only -> processed incrementally (cursor respected).
	ing.Submit(ctx, Record{App: app, Board: "score", Member: "p2", Score: 20})
	if n, _ := cons.Step(ctx); n != 1 {
		t.Fatalf("incremental step processed %d, want 1", n)
	}
	if c, _ := eng.Count(ctx, board); c != 2 {
		t.Errorf("count = %d, want 2", c)
	}
}

func TestRebuildFromLog(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	eng := engine.NewRedisEngine(rdb)
	app := ns(t)
	reg := NewStaticRegistry()
	reg.Register(engine.LogicalBoard{App: app, Board: "score"})
	board := engine.Board{Key: engine.BoardKey{App: app, Board: "score", Segment: "all", Window: "all"}}
	_ = eng.Reset(ctx, board)
	t.Cleanup(func() { _ = eng.Reset(ctx, board) })

	log := NewMemLog()
	ing := NewIngestor(log, reg, NewMemDeduper())
	for i, s := range []float64{100, 200, 50} {
		ing.Submit(ctx, Record{App: app, Board: "score", Member: "p" + string(rune('a'+i)), Score: s})
	}
	// Simulate cache loss: drain, wipe the engine, then rebuild from the log.
	NewConsumer(log, reg, eng).Drain(ctx)
	if err := eng.Reset(ctx, board); err != nil {
		t.Fatal(err)
	}
	if c, _ := eng.Count(ctx, board); c != 0 {
		t.Fatal("engine not wiped")
	}
	if err := Rebuild(ctx, log, reg, eng); err != nil {
		t.Fatal(err)
	}
	if c, _ := eng.Count(ctx, board); c != 3 {
		t.Errorf("rebuilt count = %d, want 3", c)
	}
	re, _ := eng.GetRank(ctx, board, "pb") // score 200, should be rank 1
	if re.Rank != 1 || re.Score != 200 {
		t.Errorf("after rebuild pb rank=%d score=%v, want rank 1 score 200", re.Rank, re.Score)
	}
}

func TestRedisLogAppendRead(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	stream := "lb:test:" + ns(t)
	_ = rdb.Del(ctx, stream)
	t.Cleanup(func() { _ = rdb.Del(ctx, stream) })

	log := NewRedisLog(rdb, stream, 0)
	var firstID string
	for i := 0; i < 4; i++ {
		rec := Record{App: "g", Board: "score", Member: "p", Score: float64(i), Time: time.Now().UTC()}
		if err := log.Append(ctx, &rec); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstID = rec.ID
		}
	}
	all, err := log.Read(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("read %d, want 4", len(all))
	}
	if all[0].Score != 0 || all[3].Score != 3 {
		t.Errorf("ordering wrong: %v..%v", all[0].Score, all[3].Score)
	}
	// Cursor read after the first id -> 3 remaining.
	rest, _ := log.Read(ctx, firstID, 0)
	if len(rest) != 3 || rest[0].Score != 1 {
		t.Errorf("cursor read: %d records, first %v", len(rest), rest[0].Score)
	}
}
