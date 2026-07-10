package engine

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// testEngine connects to the Redis under test (REDIS_ADDR or localhost:6379)
// and skips the test if it is unreachable.
func testEngine(t *testing.T) *RedisEngine {
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
	return NewRedisEngine(rdb)
}

// freshBoard returns a board namespaced to the test and resets it.
func freshBoard(t *testing.T, e *RedisEngine, cfg BoardConfig) Board {
	t.Helper()
	app := strings.NewReplacer("/", "-", " ", "_").Replace(t.Name())
	b := Board{Key: BoardKey{App: app, Board: "b"}, Config: cfg}
	if err := e.Reset(context.Background(), b); err != nil {
		t.Fatalf("reset: %v", err)
	}
	t.Cleanup(func() { _ = e.Reset(context.Background(), b) })
	return b
}

func members(entries []RankEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Member
	}
	return out
}

func TestSubmitAndGetRankBestDesc(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	b := freshBoard(t, e, BoardConfig{}) // defaults: desc, best, lexical

	scores := map[string]float64{"alice": 300, "bob": 500, "carol": 100}
	for m, s := range scores {
		if _, err := e.Submit(ctx, b, m, s, time.Now()); err != nil {
			t.Fatalf("submit %s: %v", m, err)
		}
	}
	// bob 500 > alice 300 > carol 100
	checkRank := func(m string, wantRank int64, wantScore float64) {
		re, err := e.GetRank(ctx, b, m)
		if err != nil {
			t.Fatalf("getrank %s: %v", m, err)
		}
		if re.Rank != wantRank || re.Score != wantScore || !re.Exact {
			t.Errorf("%s: got rank=%d score=%v exact=%v; want rank=%d score=%v", m, re.Rank, re.Score, re.Exact, wantRank, wantScore)
		}
	}
	checkRank("bob", 1, 500)
	checkRank("alice", 2, 300)
	checkRank("carol", 3, 100)
}

func TestSubmitBestKeepsHigher(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	b := freshBoard(t, e, BoardConfig{}) // best, desc

	r, _ := e.Submit(ctx, b, "p", 100, time.Now())
	if !r.Updated || r.Score != 100 {
		t.Fatalf("first submit: %+v", r)
	}
	// Lower score must NOT replace the best.
	r, _ = e.Submit(ctx, b, "p", 50, time.Now())
	if r.Updated || r.Score != 100 {
		t.Errorf("lower submit changed best: %+v", r)
	}
	// Higher score replaces.
	r, _ = e.Submit(ctx, b, "p", 150, time.Now())
	if !r.Updated || r.Score != 150 {
		t.Errorf("higher submit not applied: %+v", r)
	}
}

func TestSubmitBestAscLowerWins(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	b := freshBoard(t, e, BoardConfig{SortOrder: SortAsc}) // race times: lower wins

	e.Submit(ctx, b, "fast", 90, time.Now())
	e.Submit(ctx, b, "slow", 120, time.Now())
	// Resubmitting a worse (higher) time must not replace the best.
	r, _ := e.Submit(ctx, b, "fast", 95, time.Now())
	if r.Updated || r.Score != 90 {
		t.Errorf("asc best should keep lower time: %+v", r)
	}
	re, _ := e.GetRank(ctx, b, "fast")
	if re.Rank != 1 {
		t.Errorf("fast should rank 1, got %d", re.Rank)
	}
	// A better (lower) time replaces.
	r, _ = e.Submit(ctx, b, "slow", 80, time.Now())
	if !r.Updated || r.Score != 80 {
		t.Errorf("asc best should accept lower time: %+v", r)
	}
}

func TestUpdateLast(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	b := freshBoard(t, e, BoardConfig{UpdatePolicy: UpdateLast})
	e.Submit(ctx, b, "p", 100, time.Now())
	r, _ := e.Submit(ctx, b, "p", 50, time.Now()) // last wins even though lower
	if !r.Updated || r.Score != 50 {
		t.Errorf("last policy should overwrite: %+v", r)
	}
}

func TestUpdateIncrement(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	b := freshBoard(t, e, BoardConfig{UpdatePolicy: UpdateIncrement})
	e.Submit(ctx, b, "p", 10, time.Now())
	r, _ := e.Submit(ctx, b, "p", 5, time.Now())
	if r.Score != 15 {
		t.Errorf("increment should sum to 15, got %v", r.Score)
	}
}

func TestTopNAndPage(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	b := freshBoard(t, e, BoardConfig{})
	for i := 0; i < 10; i++ {
		e.Submit(ctx, b, "p"+strconv.Itoa(i), float64(i*10), time.Now())
	}
	top, err := e.TopN(ctx, b, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := members(top); !equalSlice(got, []string{"p9", "p8", "p7"}) {
		t.Errorf("topN = %v", got)
	}
	if top[0].Rank != 1 || top[2].Rank != 3 {
		t.Errorf("topN ranks wrong: %d..%d", top[0].Rank, top[2].Rank)
	}
	page, _ := e.Page(ctx, b, 3, 2) // ranks 4,5
	if got := members(page); !equalSlice(got, []string{"p6", "p5"}) {
		t.Errorf("page = %v", got)
	}
	if page[0].Rank != 4 {
		t.Errorf("page rank base = %d, want 4", page[0].Rank)
	}
}

func TestNeighbors(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	b := freshBoard(t, e, BoardConfig{})
	for i := 0; i < 10; i++ {
		e.Submit(ctx, b, "p"+strconv.Itoa(i), float64(i*10), time.Now())
	}
	// p5 has rank 5 (p9..p0 descending). Neighbors k=2 -> p7,p6,p5,p4,p3.
	n, err := e.Neighbors(ctx, b, "p5", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := members(n); !equalSlice(got, []string{"p7", "p6", "p5", "p4", "p3"}) {
		t.Errorf("neighbors = %v", got)
	}
	// Top member: window clamps at rank 1 (no negative ranks).
	top, _ := e.Neighbors(ctx, b, "p9", 2)
	if got := members(top); !equalSlice(got, []string{"p9", "p8", "p7"}) {
		t.Errorf("top neighbors = %v", got)
	}
	if top[0].Rank != 1 {
		t.Errorf("top neighbor rank = %d, want 1", top[0].Rank)
	}
}

func TestFriendRank(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	b := freshBoard(t, e, BoardConfig{})
	e.Submit(ctx, b, "a", 100, time.Now())
	e.Submit(ctx, b, "b", 300, time.Now())
	e.Submit(ctx, b, "c", 200, time.Now())
	e.Submit(ctx, b, "d", 999, time.Now()) // not a friend
	fr, err := e.FriendRank(ctx, b, []string{"a", "c", "b", "ghost"})
	if err != nil {
		t.Fatal(err)
	}
	// ranked among friends only: b(300) > c(200) > a(100); ghost omitted.
	if got := members(fr); !equalSlice(got, []string{"b", "c", "a"}) {
		t.Errorf("friendrank = %v", got)
	}
	if fr[0].Rank != 1 || fr[2].Rank != 3 {
		t.Errorf("friendrank positions wrong: %v", fr)
	}
}

func TestGetRankNotFound(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	b := freshBoard(t, e, BoardConfig{})
	if _, err := e.GetRank(ctx, b, "nobody"); !errors.Is(err, ErrMemberNotFound) {
		t.Errorf("expected ErrMemberNotFound, got %v", err)
	}
}

func TestRemoveAndCount(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	b := freshBoard(t, e, BoardConfig{})
	e.Submit(ctx, b, "a", 1, time.Now())
	e.Submit(ctx, b, "b", 2, time.Now())
	if c, _ := e.Count(ctx, b); c != 2 {
		t.Errorf("count = %d, want 2", c)
	}
	if err := e.Remove(ctx, b, "a"); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.Count(ctx, b); c != 1 {
		t.Errorf("count after remove = %d, want 1", c)
	}
	if _, err := e.GetRank(ctx, b, "a"); !errors.Is(err, ErrMemberNotFound) {
		t.Errorf("removed member should be gone: %v", err)
	}
}

func TestFirstToReachTieOrdering(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	b := freshBoard(t, e, BoardConfig{TieBreak: TieFirstToReach})
	base := b.Config.withDefaults().Epoch.Add(time.Hour)
	// zoe reaches 500 first; aaron reaches the same 500 later. Lexical order
	// would put aaron first; firstToReach must put zoe first.
	if _, err := e.Submit(ctx, b, "zoe", 500, base); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Submit(ctx, b, "aaron", 500, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	top, _ := e.TopN(ctx, b, 2)
	if got := members(top); !equalSlice(got, []string{"zoe", "aaron"}) {
		t.Errorf("firstToReach ordering = %v, want [zoe aaron]", got)
	}
	// Decoded score is the primary 500 for both.
	if top[0].Score != 500 || top[1].Score != 500 {
		t.Errorf("decoded scores wrong: %v", top)
	}
}

func TestSubmitBatchFanOut(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	cfg := BoardConfig{}
	lb := LogicalBoard{
		App:     strings.NewReplacer("/", "-").Replace(t.Name()),
		Board:   "b",
		Config:  cfg,
		Windows: []WindowSpec{{Kind: WindowAllTime}, {Kind: WindowDaily}},
	}
	ev := Event{Member: "p", Score: 100, Time: time.Now(), Segments: []string{"all", "region=eu"}}
	keys := DerivePhysicalBoards(lb, ev)
	ops := make([]SubmitOp, len(keys))
	for i, k := range keys {
		ops[i] = SubmitOp{Board: Board{Key: k, Config: cfg}, Member: ev.Member, Score: ev.Score, Time: ev.Time}
		t.Cleanup(func(b Board) func() { return func() { _ = e.Reset(ctx, b) } }(ops[i].Board))
	}
	res, err := e.SubmitBatch(ctx, ops)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 4 {
		t.Fatalf("expected 4 results, got %d", len(res))
	}
	// The member must now rank #1 on every physical board it fanned out to.
	for _, k := range keys {
		re, err := e.GetRank(ctx, Board{Key: k, Config: cfg}, "p")
		if err != nil {
			t.Fatalf("getrank on %s: %v", k, err)
		}
		if re.Rank != 1 {
			t.Errorf("board %s: rank=%d, want 1", k, re.Rank)
		}
	}
}

// TestPropertyRankMatchesBruteForce submits random scores and verifies the
// engine's reported rank for every member agrees with a brute-force sort.
func TestPropertyRankMatchesBruteForce(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	b := freshBoard(t, e, BoardConfig{}) // desc, best, lexical
	rng := rand.New(rand.NewSource(42))

	best := map[string]float64{}
	const n = 200
	for i := 0; i < n; i++ {
		m := "m" + strconv.Itoa(rng.Intn(50)) // 50 distinct members, repeats
		s := float64(rng.Intn(1000))
		if _, err := e.Submit(ctx, b, m, s, time.Now()); err != nil {
			t.Fatal(err)
		}
		if cur, ok := best[m]; !ok || s > cur {
			best[m] = s // best-wins reference
		}
	}
	// Brute-force ranking: sort by score desc, ties by member lexical (matches
	// Redis sorted-set tie order).
	type ms struct {
		m string
		s float64
	}
	ref := make([]ms, 0, len(best))
	for m, s := range best {
		ref = append(ref, ms{m, s})
	}
	sort.Slice(ref, func(i, j int) bool {
		if ref[i].s != ref[j].s {
			return ref[i].s > ref[j].s
		}
		// ZREVRANGE reverses the ascending lexical tie order, so equal scores
		// rank by member descending on a desc board.
		return ref[i].m > ref[j].m
	})
	for i, r := range ref {
		re, err := e.GetRank(ctx, b, r.m)
		if err != nil {
			t.Fatalf("getrank %s: %v", r.m, err)
		}
		if re.Rank != int64(i+1) {
			t.Fatalf("member %s: engine rank %d, brute-force rank %d (score %v)", r.m, re.Rank, i+1, r.s)
		}
		if re.Score != r.s {
			t.Errorf("member %s: engine score %v, reference best %v", r.m, re.Score, r.s)
		}
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRemoveFromAll(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	app := strings.NewReplacer("/", "-", " ", "_").Replace(t.Name())
	lb := LogicalBoard{App: app, Board: "b", Windows: []WindowSpec{{Kind: WindowAllTime}, {Kind: WindowDaily}}}
	now := time.Now().UTC()

	// Write alice+bob into every window/segment combo a submit would touch,
	// on two segments, plus a PAST daily window (stale but still live in the
	// cache — exactly what the reaper hasn't swept yet).
	past := now.AddDate(0, 0, -3)
	var boards []Board
	for _, ev := range []Event{
		{Member: "alice", Score: 100, Time: now, Segments: []string{"all", "region=eu"}},
		{Member: "alice", Score: 90, Time: past, Segments: []string{"all"}},
		{Member: "bob", Score: 50, Time: now, Segments: []string{"all", "region=eu"}},
	} {
		for _, k := range DerivePhysicalBoards(lb, ev) {
			b := Board{Key: k, Config: lb.Config}
			boards = append(boards, b)
			if _, err := e.Submit(ctx, b, ev.Member, ev.Score, ev.Time); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(func() {
		for _, b := range boards {
			_ = e.Reset(ctx, b)
		}
	})

	if err := e.RemoveFromAll(ctx, lb, "alice"); err != nil {
		t.Fatal(err)
	}
	for _, b := range boards {
		if _, err := e.GetRank(ctx, b, "alice"); !errors.Is(err, ErrMemberNotFound) {
			t.Errorf("alice still on %s: %v", b.Key, err)
		}
	}
	// bob is untouched on the current-window boards.
	cur := Board{Key: BoardKey{App: app, Board: "b", Segment: "all", Window: "all"}, Config: lb.Config}
	if re, err := e.GetRank(ctx, cur, "bob"); err != nil || re.Rank != 1 {
		t.Errorf("bob: %+v / %v", re, err)
	}
	// Removing an absent member is a no-op.
	if err := e.RemoveFromAll(ctx, lb, "ghost"); err != nil {
		t.Errorf("remove absent: %v", err)
	}
}

func TestRemoveFromAllMaintainsHistogram(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	cfg := BoardConfig{ApproxRank: true, ApproxMin: 0, ApproxMax: 1000, ApproxBuckets: 16}
	b := freshBoard(t, e, cfg)
	lb := LogicalBoard{App: b.Key.App, Board: b.Key.Board, Config: cfg}
	now := time.Now().UTC()
	for m, sc := range map[string]float64{"alice": 900, "bob": 500, "carol": 100} {
		if _, err := e.Submit(ctx, b, m, sc, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.RemoveFromAll(ctx, lb, "alice"); err != nil {
		t.Fatal(err)
	}
	// With alice's bucket decremented, bob's approximate rank is 1 again.
	re, err := e.GetApproxRank(ctx, b, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if re.Rank != 1 {
		t.Errorf("approx rank after removal: got %d, want 1 (histogram not decremented?)", re.Rank)
	}
}
