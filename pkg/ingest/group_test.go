package ingest

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/kodeni-am/leaderboard/pkg/engine"
	"github.com/redis/go-redis/v9"
)

// groupHarness builds a partitioned RedisLog + engine + registry for a board.
func groupHarness(t *testing.T, partitions int, cfg engine.BoardConfig) (*RedisLog, *engine.RedisEngine, *StaticRegistry, string) {
	t.Helper()
	rdb := testRedis(t)
	app := ns(t)
	prefix := "lb:test:" + app
	log := NewRedisLog(rdb, prefix, partitions, 0)
	for p := 0; p < partitions; p++ {
		_ = rdb.Del(context.Background(), log.StreamName(p))
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for p := 0; p < partitions; p++ {
			_ = rdb.Del(ctx, log.StreamName(p))
		}
		_ = engine.NewRedisEngine(rdb).Reset(ctx, engine.Board{Key: engine.BoardKey{App: app, Board: "b", Segment: "all", Window: "all"}})
	})
	reg := NewStaticRegistry()
	reg.Register(engine.LogicalBoard{App: app, Board: "b", Config: cfg})
	return log, engine.NewRedisEngine(rdb), reg, app
}

func TestPartitionRouting(t *testing.T) {
	ctx := context.Background()
	log, _, _, app := groupHarness(t, 8, engine.BoardConfig{})

	// Same (app, board, member) always routes to one partition.
	first := partitionOf(app, "b", "alice", 8)
	for i := 0; i < 5; i++ {
		rec := Record{App: app, Board: "b", Member: "alice", Score: float64(i)}
		if err := log.Append(ctx, &rec); err != nil {
			t.Fatal(err)
		}
	}
	for p := 0; p < 8; p++ {
		n, _ := log.rdb.XLen(ctx, log.StreamName(p)).Result()
		if p == first && n != 5 {
			t.Errorf("partition %d (alice's) len=%d, want 5", p, n)
		}
		if p != first && n != 0 {
			t.Errorf("partition %d len=%d, want 0 (alice should be isolated)", p, n)
		}
	}

	// Distinct members spread across more than one partition.
	seen := map[int]bool{}
	for i := 0; i < 50; i++ {
		seen[partitionOf(app, "b", "m"+strconv.Itoa(i), 8)] = true
	}
	if len(seen) < 2 {
		t.Errorf("50 members landed in %d partitions, expected spread", len(seen))
	}
}

func TestGroupConsumeAndAck(t *testing.T) {
	ctx := context.Background()
	log, eng, reg, app := groupHarness(t, 4, engine.BoardConfig{})
	for _, s := range []struct {
		m string
		v float64
	}{{"alice", 300}, {"bob", 500}, {"carol", 100}} {
		rec := Record{App: app, Board: "b", Member: s.m, Score: s.v}
		if err := log.Append(ctx, &rec); err != nil {
			t.Fatal(err)
		}
	}
	gc := NewGroupConsumer(log, reg, eng, GroupOptions{Consumer: "c1", Block: 100 * time.Millisecond})
	if err := gc.EnsureGroups(ctx); err != nil {
		t.Fatal(err)
	}
	// Drain via repeated Step (block is short).
	for i := 0; i < 6; i++ {
		if _, err := gc.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	board := engine.Board{Key: engine.BoardKey{App: app, Board: "b", Segment: "all", Window: "all"}}
	re, err := eng.GetRank(ctx, board, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if re.Rank != 1 || re.Score != 500 {
		t.Errorf("bob rank=%d score=%v, want 1/500", re.Rank, re.Score)
	}
	// All entries acked -> no pending across owned partitions.
	for _, p := range gc.Owned() {
		pend, err := log.rdb.XPending(ctx, log.StreamName(p), gc.group).Result()
		if err != nil {
			t.Fatal(err)
		}
		if pend.Count != 0 {
			t.Errorf("partition %d has %d pending, want 0", p, pend.Count)
		}
	}
}

func TestGroupIdempotentIncrement(t *testing.T) {
	ctx := context.Background()
	log, eng, reg, app := groupHarness(t, 1, engine.BoardConfig{UpdatePolicy: engine.UpdateIncrement})
	rec := Record{App: app, Board: "b", Member: "p", Score: 10}
	if err := log.Append(ctx, &rec); err != nil {
		t.Fatal(err)
	}
	gc := NewGroupConsumer(log, reg, eng, GroupOptions{Consumer: "c1"})
	if err := gc.EnsureGroups(ctx); err != nil {
		t.Fatal(err)
	}
	// Fetch the raw message and apply it twice (simulating redelivery).
	msgs, err := log.rdb.XRange(ctx, log.StreamName(0), "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gc.apply(ctx, log.StreamName(0), msgs); err != nil {
		t.Fatal(err)
	}
	if _, err := gc.apply(ctx, log.StreamName(0), msgs); err != nil {
		t.Fatal(err)
	}
	board := engine.Board{Key: engine.BoardKey{App: app, Board: "b", Segment: "all", Window: "all"}}
	re, err := eng.GetRank(ctx, board, "p")
	if err != nil {
		t.Fatal(err)
	}
	if re.Score != 10 {
		t.Errorf("idempotent increment: score=%v, want 10 (not double-counted)", re.Score)
	}
}

func TestGroupReclaimRecoversCrashedWorker(t *testing.T) {
	ctx := context.Background()
	log, eng, reg, app := groupHarness(t, 1, engine.BoardConfig{})
	rec := Record{App: app, Board: "b", Member: "ghost", Score: 777}
	if err := log.Append(ctx, &rec); err != nil {
		t.Fatal(err)
	}
	group := "rankers"
	if err := log.rdb.XGroupCreateMkStream(ctx, log.StreamName(0), group, "0").Err(); err != nil {
		t.Fatal(err)
	}
	// A "dead" worker reads the entry (now pending in its PEL) but never ACKs.
	_, err := log.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    group,
		Consumer: "dead",
		Streams:  []string{log.StreamName(0), ">"},
		Count:    10,
	}).Result()
	if err != nil {
		t.Fatal(err)
	}
	// Pending exists.
	pend, _ := log.rdb.XPending(ctx, log.StreamName(0), group).Result()
	if pend.Count != 1 {
		t.Fatalf("expected 1 pending after dead read, got %d", pend.Count)
	}
	// A live worker reclaims idle pending entries and applies them.
	gc := NewGroupConsumer(log, reg, eng, GroupOptions{Consumer: "alive", ClaimMinIdle: time.Millisecond})
	time.Sleep(10 * time.Millisecond)
	n, err := gc.Reclaim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("reclaimed %d, want 1", n)
	}
	board := engine.Board{Key: engine.BoardKey{App: app, Board: "b", Segment: "all", Window: "all"}}
	re, err := eng.GetRank(ctx, board, "ghost")
	if err != nil {
		t.Fatalf("reclaimed entry not applied: %v", err)
	}
	if re.Score != 777 {
		t.Errorf("reclaimed score=%v, want 777", re.Score)
	}
	pend, _ = log.rdb.XPending(ctx, log.StreamName(0), group).Result()
	if pend.Count != 0 {
		t.Errorf("after reclaim+ack, pending=%d, want 0", pend.Count)
	}
}

func TestGroupSelfHealsMissingGroup(t *testing.T) {
	ctx := context.Background()
	log, eng, reg, app := groupHarness(t, 1, engine.BoardConfig{})
	rec := Record{App: app, Board: "b", Member: "phoenix", Score: 42}
	if err := log.Append(ctx, &rec); err != nil {
		t.Fatal(err)
	}
	gc := NewGroupConsumer(log, reg, eng, GroupOptions{Consumer: "c1", Block: 50 * time.Millisecond})
	if err := gc.EnsureGroups(ctx); err != nil {
		t.Fatal(err)
	}
	// Simulate a Redis flush/restart: the group disappears.
	if err := log.rdb.XGroupDestroy(ctx, log.StreamName(0), "rankers").Err(); err != nil {
		t.Fatal(err)
	}
	// Step must not error on NOGROUP — it recreates the group and recovers.
	for i := 0; i < 5; i++ {
		if _, err := gc.Step(ctx); err != nil {
			t.Fatalf("step after group destroy should self-heal, got: %v", err)
		}
	}
	board := engine.Board{Key: engine.BoardKey{App: app, Board: "b", Segment: "all", Window: "all"}}
	if re, err := eng.GetRank(ctx, board, "phoenix"); err != nil {
		t.Fatalf("record not applied after self-heal: %v", err)
	} else if re.Member != "phoenix" {
		t.Errorf("unexpected entry %+v", re)
	}
}

func TestGroupStaticOwnershipParallel(t *testing.T) {
	ctx := context.Background()
	log, eng, reg, app := groupHarness(t, 6, engine.BoardConfig{})

	// 30 distinct members spread across the 6 partitions.
	for i := 0; i < 30; i++ {
		rec := Record{App: app, Board: "b", Member: "m" + strconv.Itoa(i), Score: float64(i)}
		if err := log.Append(ctx, &rec); err != nil {
			t.Fatal(err)
		}
	}
	// Two workers split the partitions disjointly.
	w0 := NewGroupConsumer(log, reg, eng, GroupOptions{Consumer: "w0", WorkerIndex: 0, WorkerCount: 2, Block: 100 * time.Millisecond})
	w1 := NewGroupConsumer(log, reg, eng, GroupOptions{Consumer: "w1", WorkerIndex: 1, WorkerCount: 2, Block: 100 * time.Millisecond})

	// Ownership is disjoint and covers all partitions.
	owned := map[int]int{}
	for _, p := range w0.Owned() {
		owned[p]++
	}
	for _, p := range w1.Owned() {
		owned[p]++
	}
	if len(owned) != 6 {
		t.Fatalf("partitions covered = %d, want 6", len(owned))
	}
	for p, c := range owned {
		if c != 1 {
			t.Fatalf("partition %d owned by %d workers, want exactly 1", p, c)
		}
	}
	for _, w := range []*GroupConsumer{w0, w1} {
		if err := w.EnsureGroups(ctx); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 6; i++ {
			if _, err := w.Step(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	// All 30 members applied exactly once (no gaps from missed partitions, no
	// double-processing from overlapping ownership).
	board := engine.Board{Key: engine.BoardKey{App: app, Board: "b", Segment: "all", Window: "all"}}
	total, err := eng.Count(ctx, board)
	if err != nil {
		t.Fatal(err)
	}
	if total != 30 {
		t.Errorf("applied %d members, want 30 (no gaps, no double-processing)", total)
	}
}

func TestGroupConsumerAppliesTombstones(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	eng := engine.NewRedisEngine(rdb)
	app := ns(t)

	reg := NewStaticRegistry()
	reg.Register(engine.LogicalBoard{App: app, Board: "score"})
	rlog := NewRedisLog(rdb, "test:"+app, 1, 0)
	t.Cleanup(func() { rdb.Del(ctx, rlog.StreamName(0)) })
	ing := NewIngestor(rlog, reg, NewMemDeduper())
	now := time.Now().UTC()
	b := engine.Board{Key: engine.BoardKey{App: app, Board: "score", Segment: "all", Window: "all"}}
	t.Cleanup(func() { _ = eng.Reset(ctx, b) })

	_, _ = ing.Submit(ctx, Record{App: app, Board: "score", Member: "alice", Score: 300, Time: now})
	if err := ing.Remove(ctx, Record{App: app, Board: "score", Member: "alice", Time: now}); err != nil {
		t.Fatal(err)
	}

	gc := NewGroupConsumer(rlog, reg, eng, GroupOptions{Group: "g-" + app})
	if err := gc.EnsureGroups(ctx); err != nil {
		t.Fatal(err)
	}
	for {
		n, err := gc.Step(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	if _, err := eng.GetRank(ctx, b, "alice"); !errors.Is(err, engine.ErrMemberNotFound) {
		t.Errorf("tombstone not applied by group consumer: %v", err)
	}
}
