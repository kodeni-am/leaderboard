package engine

import (
	"context"
	"strconv"

	"github.com/redis/go-redis/v9"
)

// Histogram is the approximate-rank tier (the seam for boards too large for a
// single sorted set). It maintains a fixed-bucket score distribution in a Redis
// hash via O(1) HINCRBY — no Redis modules required, so it runs on stock
// ElastiCache/Valkey. Given a score, it estimates how many members rank ahead,
// yielding an O(buckets) approximate global rank without scanning the set.
//
// SP1 ships and tests this primitive standalone. Wiring it into a sharded
// multi-node board (member-hash partitioning + per-shard exact top-N merge) is
// a benchmarked follow-on; the research could not confirm the single-set
// breakpoint, so we measure before building that orchestration.
type Histogram struct {
	rdb     redis.UniversalClient
	key     string
	min     float64
	max     float64
	buckets int
}

// NewHistogram creates a histogram over [min,max] split into `buckets` equal
// bins, backed by the board's :h key.
func NewHistogram(rdb redis.UniversalClient, b BoardKey, min, max float64, buckets int) *Histogram {
	if buckets < 1 {
		buckets = 1
	}
	return &Histogram{rdb: rdb, key: b.hKey(), min: min, max: max, buckets: buckets}
}

// boardHistogram builds the histogram for a board from its approx-rank config.
// Callers must have verified cfg.ApproxRank is enabled.
func boardHistogram(rdb redis.UniversalClient, b Board) *Histogram {
	cfg := b.Config.withDefaults()
	return NewHistogram(rdb, b.Key, cfg.ApproxMin, cfg.ApproxMax, cfg.ApproxBuckets)
}

// QueueDelta adds `delta` to the bucket for `score` inside an existing pipeline,
// so histogram maintenance rides along with the score write in one round trip.
// The :h key shares the board's hash tag with :z, so this stays single-slot on
// a cluster.
func (h *Histogram) QueueDelta(ctx context.Context, pipe redis.Pipeliner, score float64, delta int64) {
	pipe.HIncrBy(ctx, h.key, strconv.Itoa(h.bucketIndex(score)), delta)
}

// bucketIndex clamps a score into [0, buckets-1].
func (h *Histogram) bucketIndex(score float64) int {
	if score <= h.min {
		return 0
	}
	if score >= h.max {
		return h.buckets - 1
	}
	frac := (score - h.min) / (h.max - h.min)
	idx := int(frac * float64(h.buckets))
	if idx >= h.buckets {
		idx = h.buckets - 1
	}
	return idx
}

// Add records a score. Remove records its departure (use delta -1).
func (h *Histogram) Add(ctx context.Context, score float64) error {
	return h.add(ctx, score, 1)
}

func (h *Histogram) Remove(ctx context.Context, score float64) error {
	return h.add(ctx, score, -1)
}

func (h *Histogram) add(ctx context.Context, score float64, delta int64) error {
	field := strconv.Itoa(h.bucketIndex(score))
	return h.rdb.HIncrBy(ctx, h.key, field, delta).Err()
}

// ApproxRankDesc estimates the 1-based rank of `score` for descending boards:
// (members in strictly-higher buckets) + 1. Resolution is one bucket width.
func (h *Histogram) ApproxRankDesc(ctx context.Context, score float64) (int64, error) {
	counts, err := h.counts(ctx)
	if err != nil {
		return 0, err
	}
	idx := h.bucketIndex(score)
	var ahead int64
	for i := idx + 1; i < h.buckets; i++ {
		ahead += counts[i]
	}
	return ahead + 1, nil
}

// ApproxRankAsc estimates the 1-based rank for ascending boards (lower=better):
// (members in strictly-lower buckets) + 1.
func (h *Histogram) ApproxRankAsc(ctx context.Context, score float64) (int64, error) {
	counts, err := h.counts(ctx)
	if err != nil {
		return 0, err
	}
	idx := h.bucketIndex(score)
	var ahead int64
	for i := 0; i < idx; i++ {
		ahead += counts[i]
	}
	return ahead + 1, nil
}

// approxRankFromCounts computes the 1-based approximate rank of the bucket `idx`
// from total bucket counts (which may be summed across shards). Descending: the
// members in strictly-higher buckets rank ahead; ascending: strictly-lower.
func approxRankFromCounts(counts []int64, idx int, asc bool) int64 {
	var ahead int64
	if asc {
		for i := 0; i < idx && i < len(counts); i++ {
			ahead += counts[i]
		}
	} else {
		for i := idx + 1; i < len(counts); i++ {
			ahead += counts[i]
		}
	}
	return ahead + 1
}

func (h *Histogram) counts(ctx context.Context) ([]int64, error) {
	all, err := h.rdb.HGetAll(ctx, h.key).Result()
	if err != nil {
		return nil, err
	}
	return h.countsFrom(all)
}

// countsFrom turns a raw HGETALL result (field->count) into a dense bucket
// slice. Split out so callers can pipeline many HGETALLs and decode the results.
func (h *Histogram) countsFrom(all map[string]string) ([]int64, error) {
	counts := make([]int64, h.buckets)
	for f, v := range all {
		idx, err := strconv.Atoi(f)
		if err != nil || idx < 0 || idx >= h.buckets {
			continue
		}
		n, _ := strconv.ParseInt(v, 10, 64)
		counts[idx] = n
	}
	return counts, nil
}
