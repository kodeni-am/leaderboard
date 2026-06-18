package engine

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// shardedFixture connects to Redis (skipping if unavailable) and returns a
// single-set engine (ground truth) plus a sharded engine, each on its own
// freshly-reset board, so sharded results can be compared against exact ones.
func shardedFixture(t *testing.T, shards int, cfg BoardConfig) (*RedisEngine, *ShardedEngine, Board, Board) {
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
	re := NewRedisEngine(rdb)
	se := NewShardedEngine(rdb, shards)

	app := sanitize(t.Name())
	truth := Board{Key: BoardKey{App: app, Board: "truth"}, Config: cfg}
	shard := Board{Key: BoardKey{App: app, Board: "shard"}, Config: cfg}
	reset := func() {
		_ = re.Reset(context.Background(), truth)
		_ = se.Reset(context.Background(), shard)
	}
	reset()
	t.Cleanup(reset)
	return re, se, truth, shard
}

func sanitize(s string) string {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch r {
		case '/', ' ', ':', '{', '}':
			out = append(out, '-')
		default:
			out = append(out, byte(r))
		}
	}
	return string(out)
}

// seedBoth submits the same (member,score) set to both engines.
func seedBoth(t *testing.T, re *RedisEngine, se *ShardedEngine, truth, shard Board, members map[string]float64) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	for m, v := range members {
		if _, err := re.Submit(ctx, truth, m, v, now); err != nil {
			t.Fatal(err)
		}
		if _, err := se.Submit(ctx, shard, m, v, now); err != nil {
			t.Fatal(err)
		}
	}
}

func sameEntries(t *testing.T, label string, got, want []RankEntry) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d entries, want %d\n got=%v\nwant=%v", label, len(got), len(want), got, want)
	}
	for i := range got {
		if got[i].Member != want[i].Member || got[i].Score != want[i].Score {
			t.Errorf("%s[%d]: got (%s,%v) want (%s,%v)", label, i, got[i].Member, got[i].Score, want[i].Member, want[i].Score)
		}
	}
}

func TestShardedTopPageCountMatchExact(t *testing.T) {
	re, se, truth, shard := shardedFixture(t, 8, BoardConfig{SortOrder: SortDesc, UpdatePolicy: UpdateBest})
	ctx := context.Background()

	// Distinct scores -> unambiguous ordering for an exact comparison.
	members := map[string]float64{}
	for i := 0; i < 200; i++ {
		members[fmt.Sprintf("m%03d", i)] = float64(i) // unique scores 0..199
	}
	seedBoth(t, re, se, truth, shard, members)

	// Count.
	gotCount, err := se.Count(ctx, shard)
	if err != nil {
		t.Fatal(err)
	}
	if gotCount != 200 {
		t.Errorf("sharded count=%d, want 200", gotCount)
	}

	// TopN matches the single-set engine exactly.
	for _, n := range []int{1, 5, 25, 200} {
		want, err := re.TopN(ctx, truth, n)
		if err != nil {
			t.Fatal(err)
		}
		got, err := se.TopN(ctx, shard, n)
		if err != nil {
			t.Fatal(err)
		}
		sameEntries(t, fmt.Sprintf("TopN(%d)", n), got, want)
		if len(got) > 0 && got[0].Member != "m199" {
			t.Errorf("top member=%s, want m199", got[0].Member)
		}
	}

	// Page matches too.
	for _, p := range []struct{ off, lim int }{{0, 10}, {10, 10}, {50, 30}, {190, 50}} {
		want, _ := re.Page(ctx, truth, p.off, p.lim)
		got, _ := se.Page(ctx, shard, p.off, p.lim)
		sameEntries(t, fmt.Sprintf("Page(%d,%d)", p.off, p.lim), got, want)
	}
}

func TestShardedAscMatchesExact(t *testing.T) {
	re, se, truth, shard := shardedFixture(t, 5, BoardConfig{SortOrder: SortAsc, UpdatePolicy: UpdateBest})
	ctx := context.Background()
	members := map[string]float64{}
	for i := 0; i < 60; i++ {
		members[fmt.Sprintf("p%02d", i)] = float64(i * 10)
	}
	seedBoth(t, re, se, truth, shard, members)

	want, _ := re.TopN(ctx, truth, 60)
	got, _ := se.TopN(ctx, shard, 60)
	sameEntries(t, "asc TopN", got, want)
	if got[0].Member != "p00" { // lowest score ranks first
		t.Errorf("asc top=%s, want p00", got[0].Member)
	}
}

func TestShardedFirstToReachTieOrderAcrossShards(t *testing.T) {
	cfg := BoardConfig{SortOrder: SortDesc, UpdatePolicy: UpdateLast, TieBreak: TieFirstToReach, ScoreBits: 20}
	re, se, truth, shard := shardedFixture(t, 4, cfg)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Everyone ties on score 100; earlier achiever ranks first. Members are
	// chosen to spread across shards, so this exercises cross-shard tie order.
	for i := 0; i < 20; i++ {
		m := fmt.Sprintf("tie%02d", i)
		ts := base.Add(time.Duration(i) * time.Minute) // m00 earliest
		if _, err := re.Submit(ctx, truth, m, 100, ts); err != nil {
			t.Fatal(err)
		}
		if _, err := se.Submit(ctx, shard, m, 100, ts); err != nil {
			t.Fatal(err)
		}
	}
	want, _ := re.TopN(ctx, truth, 20)
	got, _ := se.TopN(ctx, shard, 20)
	sameEntries(t, "firstToReach TopN", got, want)
	if got[0].Member != "tie00" {
		t.Errorf("earliest achiever should rank 1, got %s", got[0].Member)
	}
}

func TestShardedFriendRankMatchesExact(t *testing.T) {
	re, se, truth, shard := shardedFixture(t, 6, BoardConfig{SortOrder: SortDesc, UpdatePolicy: UpdateBest})
	ctx := context.Background()
	members := map[string]float64{}
	for i := 0; i < 100; i++ {
		members[fmt.Sprintf("u%03d", i)] = float64(i)
	}
	seedBoth(t, re, se, truth, shard, members)

	friends := []string{"u010", "u090", "u050", "ghost", "u001"}
	want, _ := re.FriendRank(ctx, truth, friends)
	got, _ := se.FriendRank(ctx, shard, friends)
	sameEntries(t, "FriendRank", got, want)
	if len(got) != 4 { // ghost is absent
		t.Errorf("friend count=%d, want 4", len(got))
	}
}

func TestShardedApproxRankWithinBucket(t *testing.T) {
	cfg := BoardConfig{SortOrder: SortDesc, UpdatePolicy: UpdateBest, ApproxRank: true, ApproxMin: 0, ApproxMax: 1000, ApproxBuckets: 100}
	re, se, truth, shard := shardedFixture(t, 8, cfg)
	ctx := context.Background()

	rng := rand.New(rand.NewSource(424242))
	members := map[string]float64{}
	for i := 0; i < 1500; i++ {
		members[fmt.Sprintf("m%04d", i)] = float64(rng.Intn(1000))
	}
	seedBoth(t, re, se, truth, shard, members)

	h := NewHistogram(se.rdb, BoardKey{}, 0, 1000, 100)
	pop := map[int]int{}
	for _, v := range members {
		pop[h.bucketIndex(v)]++
	}

	for i := 0; i < 80; i++ {
		m := fmt.Sprintf("m%04d", rng.Intn(1500))
		approx, err := se.GetApproxRank(ctx, shard, m)
		if err != nil {
			t.Fatal(err)
		}
		if approx.Exact {
			t.Errorf("%s: Exact=true, want false", m)
		}
		exact, err := re.GetRank(ctx, truth, m) // ground-truth exact rank
		if err != nil {
			t.Fatal(err)
		}
		diff := exact.Rank - approx.Rank
		bound := int64(pop[h.bucketIndex(members[m])])
		if diff < 0 || diff >= bound {
			t.Errorf("%s (score %v): exact=%d approx=%d diff=%d, want 0<=diff<%d",
				m, members[m], exact.Rank, approx.Rank, diff, bound)
		}
	}
}

func TestShardedApproxRankDisabled(t *testing.T) {
	_, se, _, shard := shardedFixture(t, 4, BoardConfig{SortOrder: SortDesc})
	ctx := context.Background()
	if _, err := se.Submit(ctx, shard, "x", 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := se.GetApproxRank(ctx, shard, "x"); !errors.Is(err, ErrApproxDisabled) {
		t.Errorf("approx on non-approx sharded board: err=%v, want ErrApproxDisabled", err)
	}
}

func TestShardedNeighborsMatchExactMembers(t *testing.T) {
	cfg := BoardConfig{SortOrder: SortDesc, UpdatePolicy: UpdateBest, ApproxRank: true, ApproxMin: 0, ApproxMax: 1000, ApproxBuckets: 1000}
	re, se, truth, shard := shardedFixture(t, 8, cfg)
	ctx := context.Background()

	members := map[string]float64{}
	for i := 0; i < 300; i++ {
		members[fmt.Sprintf("m%03d", i)] = float64(i) // distinct -> unambiguous neighbors
	}
	seedBoth(t, re, se, truth, shard, members)

	for _, target := range []string{"m150", "m000", "m299", "m005"} {
		want, err := re.Neighbors(ctx, truth, target, 3)
		if err != nil {
			t.Fatal(err)
		}
		got, err := se.Neighbors(ctx, shard, target, 3)
		if err != nil {
			t.Fatal(err)
		}
		// Membership/order within the window must match the exact engine.
		sameEntries(t, "Neighbors("+target+")", got, want)
		// The target sits in the middle (except at the edges).
		mid := -1
		for i, e := range got {
			if e.Member == target {
				mid = i
			}
		}
		if mid < 0 {
			t.Errorf("Neighbors(%s): target not in window %v", target, got)
		}
	}
}

func TestShardedRoutingStableAndUpdates(t *testing.T) {
	_, se, _, shard := shardedFixture(t, 8, BoardConfig{SortOrder: SortDesc, UpdatePolicy: UpdateBest})
	ctx := context.Background()

	// A member always routes to the same shard.
	if a, b := se.shardOf("steady"), se.shardOf("steady"); a != b {
		t.Fatalf("shardOf not stable: %d vs %d", a, b)
	}
	// Members spread across more than one shard.
	seen := map[int]bool{}
	for i := 0; i < 100; i++ {
		seen[se.shardOf(fmt.Sprintf("m%d", i))] = true
	}
	if len(seen) < 2 {
		t.Errorf("100 members hit %d shards, expected spread", len(seen))
	}

	// best-policy update on the same member stays on one shard and keeps the best.
	if _, err := se.Submit(ctx, shard, "hero", 100, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := se.Submit(ctx, shard, "hero", 50, time.Now()); err != nil { // not better
		t.Fatal(err)
	}
	if _, err := se.Submit(ctx, shard, "hero", 300, time.Now()); err != nil { // better
		t.Fatal(err)
	}
	top, err := se.TopN(ctx, shard, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 1 || top[0].Member != "hero" || top[0].Score != 300 {
		t.Errorf("after best updates: %+v, want hero/300", top)
	}
	if c, _ := se.Count(ctx, shard); c != 1 {
		t.Errorf("count=%d, want 1 (one member despite 3 submits)", c)
	}

	// Remove routes correctly and clears the member.
	if err := se.Remove(ctx, shard, "hero"); err != nil {
		t.Fatal(err)
	}
	if c, _ := se.Count(ctx, shard); c != 0 {
		t.Errorf("count after remove=%d, want 0", c)
	}
}
