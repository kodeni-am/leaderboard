package engine

import (
	"context"
	"strings"
	"testing"
)

func TestHistogramApproxRank(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	app := strings.NewReplacer("/", "-").Replace(t.Name())
	bk := BoardKey{App: app, Board: "b"}
	_ = e.rdb.Del(ctx, bk.hKey())
	t.Cleanup(func() { _ = e.rdb.Del(ctx, bk.hKey()) })

	h := NewHistogram(e.rdb, bk, 0, 1000, 10) // 10 buckets of width 100

	// Distribution: 100 members in [0,100), 100 in [100,200), ... up to [900,1000).
	for bucket := 0; bucket < 10; bucket++ {
		score := float64(bucket*100 + 50)
		for i := 0; i < 100; i++ {
			if err := h.Add(ctx, score); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A score of 950 sits in the top bucket; ~0 members rank ahead -> rank ~1.
	r, err := h.ApproxRankDesc(ctx, 950)
	if err != nil {
		t.Fatal(err)
	}
	if r != 1 {
		t.Errorf("approx rank for top-bucket score = %d, want 1", r)
	}
	// A score of 550 is in bucket 5; buckets 6,7,8,9 = 400 members rank ahead.
	r, _ = h.ApproxRankDesc(ctx, 550)
	if r != 401 {
		t.Errorf("approx rank for mid score = %d, want 401", r)
	}
	// Ascending interpretation: for 550, buckets 0..4 = 500 members are "ahead".
	r, _ = h.ApproxRankAsc(ctx, 550)
	if r != 501 {
		t.Errorf("approx asc rank = %d, want 501", r)
	}
	// Removing decrements the bucket.
	for i := 0; i < 100; i++ {
		_ = h.Remove(ctx, 950)
	}
	r, _ = h.ApproxRankDesc(ctx, 850)
	if r != 1 { // top bucket now empty, 850 in bucket 8 with bucket 9 empty
		t.Errorf("after removal, rank for 850 = %d, want 1", r)
	}
}
