package engine

import (
	"context"
	"errors"
	"math/rand"
	"strconv"
	"testing"
	"time"
)

// approxCfg returns a config with the approximate-rank tier enabled over
// [0,1000] with bucket width 1, so the widely-spaced test scores below each
// land in their own bucket and approx rank equals exact rank.
func approxCfg(order SortOrder, policy UpdatePolicy) BoardConfig {
	return BoardConfig{
		SortOrder:     order,
		UpdatePolicy:  policy,
		ApproxRank:    true,
		ApproxMin:     0,
		ApproxMax:     1000,
		ApproxBuckets: 1000,
	}
}

func TestApproxRankMatchesExactDesc(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	b := freshBoard(t, e, approxCfg(SortDesc, UpdateBest))

	// Distinct buckets: scores 100,200,...,500.
	want := map[string]int64{} // member -> expected rank
	for i := 1; i <= 5; i++ {
		m := "m" + strconv.Itoa(i)
		if _, err := e.Submit(ctx, b, m, float64(i*100), time.Now()); err != nil {
			t.Fatal(err)
		}
		want[m] = int64(6 - i) // m5 (500) is rank 1, m1 (100) is rank 5
	}
	for m, exp := range want {
		re, err := e.GetApproxRank(ctx, b, m)
		if err != nil {
			t.Fatalf("approx rank %s: %v", m, err)
		}
		if re.Exact {
			t.Errorf("%s: Exact=true, want false for approx tier", m)
		}
		if re.Rank != exp {
			t.Errorf("%s: approx rank=%d, want %d", m, re.Rank, exp)
		}
		// Agrees with the exact rank (distinct buckets => no approximation error).
		ex, err := e.GetRank(ctx, b, m)
		if err != nil {
			t.Fatal(err)
		}
		if re.Rank != ex.Rank {
			t.Errorf("%s: approx %d != exact %d", m, re.Rank, ex.Rank)
		}
	}
}

func TestApproxRankMatchesExactAsc(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	b := freshBoard(t, e, approxCfg(SortAsc, UpdateBest))

	for i := 1; i <= 5; i++ {
		if _, err := e.Submit(ctx, b, "m"+strconv.Itoa(i), float64(i*100), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	// Ascending: lowest score ranks first, so m1 (100) is rank 1.
	for i := 1; i <= 5; i++ {
		m := "m" + strconv.Itoa(i)
		re, err := e.GetApproxRank(ctx, b, m)
		if err != nil {
			t.Fatal(err)
		}
		if re.Rank != int64(i) {
			t.Errorf("%s: approx rank=%d, want %d", m, re.Rank, i)
		}
	}
}

func TestApproxRankBestDoesNotMoveOnNonImprovement(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	b := freshBoard(t, e, approxCfg(SortDesc, UpdateBest))

	if _, err := e.Submit(ctx, b, "alice", 300, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Non-improving write: score stays 300, histogram must not double-count.
	if _, err := e.Submit(ctx, b, "alice", 50, time.Now()); err != nil {
		t.Fatal(err)
	}
	h := boardHistogram(e.rdb, b)
	counts, err := h.counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, c := range counts {
		total += c
	}
	if total != 1 {
		t.Errorf("histogram total=%d, want 1 (one member, no double-count)", total)
	}
	// And the bucket for 50 is empty (the non-improving score was discarded).
	if counts[h.bucketIndex(50)] != 0 {
		t.Errorf("bucket for discarded score 50 has count %d, want 0", counts[h.bucketIndex(50)])
	}

	// An improving write moves alice's bucket from 300 -> 600.
	if _, err := e.Submit(ctx, b, "alice", 600, time.Now()); err != nil {
		t.Fatal(err)
	}
	counts, _ = h.counts(ctx)
	if counts[h.bucketIndex(300)] != 0 {
		t.Errorf("old bucket (300) count=%d, want 0 after improvement", counts[h.bucketIndex(300)])
	}
	if counts[h.bucketIndex(600)] != 1 {
		t.Errorf("new bucket (600) count=%d, want 1", counts[h.bucketIndex(600)])
	}
}

func TestApproxRankIncrementMovesBucket(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	b := freshBoard(t, e, approxCfg(SortDesc, UpdateIncrement))

	if _, err := e.Submit(ctx, b, "p", 10, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Submit(ctx, b, "p", 5, time.Now()); err != nil { // -> 15
		t.Fatal(err)
	}
	h := boardHistogram(e.rdb, b)
	counts, err := h.counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts[h.bucketIndex(10)] != 0 {
		t.Errorf("bucket 10 count=%d, want 0 (member moved)", counts[h.bucketIndex(10)])
	}
	if counts[h.bucketIndex(15)] != 1 {
		t.Errorf("bucket 15 count=%d, want 1", counts[h.bucketIndex(15)])
	}
}

func TestApproxRankRemoveDecrements(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	b := freshBoard(t, e, approxCfg(SortDesc, UpdateBest))

	for i := 1; i <= 3; i++ {
		if _, err := e.Submit(ctx, b, "m"+strconv.Itoa(i), float64(i*100), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	// Remove the top member (m3 @ 300). m2 should become rank 1.
	if err := e.Remove(ctx, b, "m3"); err != nil {
		t.Fatal(err)
	}
	re, err := e.GetApproxRank(ctx, b, "m2")
	if err != nil {
		t.Fatal(err)
	}
	if re.Rank != 1 {
		t.Errorf("after removing top member, m2 approx rank=%d, want 1", re.Rank)
	}
	h := boardHistogram(e.rdb, b)
	counts, _ := h.counts(ctx)
	if counts[h.bucketIndex(300)] != 0 {
		t.Errorf("removed member's bucket count=%d, want 0", counts[h.bucketIndex(300)])
	}
}

func TestApproxRankDisabledAndNotFound(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()

	// Disabled by default.
	plain := freshBoard(t, e, BoardConfig{})
	if _, err := e.Submit(ctx, plain, "x", 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.GetApproxRank(ctx, plain, "x"); !errors.Is(err, ErrApproxDisabled) {
		t.Errorf("GetApproxRank on non-approx board: err=%v, want ErrApproxDisabled", err)
	}

	// Enabled board, missing member.
	ab := freshBoard(t, e, approxCfg(SortDesc, UpdateBest))
	if _, err := e.GetApproxRank(ctx, ab, "ghost"); !errors.Is(err, ErrMemberNotFound) {
		t.Errorf("GetApproxRank for missing member: err=%v, want ErrMemberNotFound", err)
	}
}

// TestApproxRankWithinBucketResolution checks the approximation guarantee on a
// dense distribution: the histogram places a member at the top of its bucket,
// so the exact rank is at most (bucket population - 1) higher than the estimate
// and never lower. Coarse buckets (width 10 here) make the bound non-trivial.
func TestApproxRankWithinBucketResolution(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	cfg := approxCfg(SortDesc, UpdateBest)
	cfg.ApproxBuckets = 100 // width 10 over [0,1000]
	b := freshBoard(t, e, cfg)

	rng := rand.New(rand.NewSource(20260618))
	const n = 2000
	scores := make(map[string]float64, n)
	for i := 0; i < n; i++ {
		m := "m" + strconv.Itoa(i)
		s := float64(rng.Intn(1000))
		scores[m] = s
		if _, err := e.Submit(ctx, b, m, s, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	h := boardHistogram(e.rdb, b)

	// Per-bucket population, to bound the error for each sampled member.
	bucketPop := make(map[int]int)
	for _, s := range scores {
		bucketPop[h.bucketIndex(s)]++
	}

	for i := 0; i < 100; i++ {
		m := "m" + strconv.Itoa(rng.Intn(n))
		approx, err := e.GetApproxRank(ctx, b, m)
		if err != nil {
			t.Fatal(err)
		}
		exact, err := e.GetRank(ctx, b, m)
		if err != nil {
			t.Fatal(err)
		}
		diff := exact.Rank - approx.Rank
		pop := int64(bucketPop[h.bucketIndex(scores[m])])
		if diff < 0 || diff >= pop {
			t.Errorf("%s (score %v): exact=%d approx=%d diff=%d, want 0<=diff<%d (bucket pop)",
				m, scores[m], exact.Rank, approx.Rank, diff, pop)
		}
	}
}

func TestApproxRankConfigValidation(t *testing.T) {
	// min must be < max.
	bad := BoardConfig{ApproxRank: true, ApproxMin: 100, ApproxMax: 100}
	if err := bad.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("equal min/max: err=%v, want ErrInvalidConfig", err)
	}
	// Valid config with default bucket count.
	ok := BoardConfig{ApproxRank: true, ApproxMin: 0, ApproxMax: 10}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid approx config rejected: %v", err)
	}
	if got := ok.withDefaults().ApproxBuckets; got != 1024 {
		t.Errorf("default ApproxBuckets=%d, want 1024", got)
	}
}
