package ingest

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kodeni-am/leaderboard/pkg/engine"
	"github.com/redis/go-redis/v9"
)

// testCluster connects to a Redis Cluster given by REDIS_CLUSTER_ADDRS
// (comma-separated host:port seeds) and skips the test if none is reachable.
// This mirrors the single-node redis-skip pattern but targets a real cluster so
// CI/dev without one stays green.
func testCluster(t *testing.T) redis.UniversalClient {
	t.Helper()
	raw := os.Getenv("REDIS_CLUSTER_ADDRS")
	if raw == "" {
		t.Skip("REDIS_CLUSTER_ADDRS not set; skipping Redis Cluster smoke test")
	}
	addrs := strings.Split(raw, ",")
	for i := range addrs {
		addrs[i] = strings.TrimSpace(addrs[i])
	}
	// >1 addr makes NewUniversalClient pick the ClusterClient, which is exactly
	// the production path: cmd/leaderboardd builds the same way from REDIS_ADDR.
	rdb := redis.NewUniversalClient(&redis.UniversalOptions{Addrs: addrs})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("redis cluster not available at %s: %v", raw, err)
	}
	return rdb
}

// TestClusterIngestNoCrossSlot is the regression guard for the per-stream read
// fix. The partition streams lb:ingest:<p> hash to different slots, so the old
// single XREADGROUP over all of them returned CROSSSLOT on a cluster. With the
// per-stream Step, draining works and every record lands on the right board.
func TestClusterIngestNoCrossSlot(t *testing.T) {
	rdb := testCluster(t)
	ctx := context.Background()
	eng := engine.NewRedisEngine(rdb)
	app := ns(t)
	prefix := "lb:test:" + app
	const partitions = 8

	log := NewRedisLog(rdb, prefix, partitions, 0)
	reg := NewStaticRegistry()
	reg.Register(engine.LogicalBoard{App: app, Board: "b", Config: engine.BoardConfig{}})

	cleanup := func() {
		for p := 0; p < partitions; p++ {
			_ = rdb.Del(ctx, log.StreamName(p))
		}
		_ = eng.Reset(ctx, engine.Board{Key: engine.BoardKey{App: app, Board: "b", Segment: "all", Window: "all"}})
	}
	cleanup()
	t.Cleanup(cleanup)

	// 40 distinct members spread across the partitions (and thus across slots).
	const n = 40
	for i := 0; i < n; i++ {
		rec := Record{App: app, Board: "b", Member: "m" + strconv.Itoa(i), Score: float64(i)}
		if err := log.Append(ctx, &rec); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	// One consumer owns every partition. Step reads each owned stream on its own
	// slot — no multi-key command spans slots.
	gc := NewGroupConsumer(log, reg, eng, GroupOptions{Consumer: "cluster", Block: 100 * time.Millisecond})
	if err := gc.EnsureGroups(ctx); err != nil {
		t.Fatalf("ensure groups: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		applied, err := gc.Step(ctx)
		if err != nil {
			t.Fatalf("step on cluster (CROSSSLOT regression?): %v", err)
		}
		board := engine.Board{Key: engine.BoardKey{App: app, Board: "b", Segment: "all", Window: "all"}}
		if total, err := eng.Count(ctx, board); err == nil && total == n {
			break
		}
		if applied == 0 {
			time.Sleep(20 * time.Millisecond)
		}
	}

	board := engine.Board{Key: engine.BoardKey{App: app, Board: "b", Segment: "all", Window: "all"}}
	total, err := eng.Count(ctx, board)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != n {
		t.Fatalf("applied %d members on cluster, want %d", total, n)
	}

	// Top member is m39 (highest score) at rank 1 — proves reads work too.
	re, err := eng.GetRank(ctx, board, "m"+strconv.Itoa(n-1))
	if err != nil {
		t.Fatalf("get rank on cluster: %v", err)
	}
	if re.Rank != 1 {
		t.Errorf("top member rank=%d, want 1", re.Rank)
	}

	// Reclaim is also per-stream; it must not CROSSSLOT either.
	if _, err := gc.Reclaim(ctx); err != nil {
		t.Fatalf("reclaim on cluster: %v", err)
	}
}
