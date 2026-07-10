package engine

import (
	"context"
	"hash/fnv"
	"sort"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// ShardedEngine splits a single logical board across N physical sorted sets
// ("shards") so one board can exceed a single Redis node's capacity. Members are
// assigned to shards by a stable hash, so a member always lands on the same
// shard. Each shard is an independent RedisEngine board with its own hash tag,
// so shards spread across a cluster.
//
// What stays exact and what becomes approximate:
//   - Submit / Remove / Count / FriendRank — exact (routed or summed).
//   - TopN / Page — exact, via a k-way merge of each shard's top range (the
//     global top-K is contained in the union of the per-shard top-Ks).
//   - GetRank / GetApproxRank — approximate (Exact=false), summed from the
//     per-shard score histograms. Sharded boards must enable ApproxRank; exact
//     global rank across shards is intentionally not offered (a board big enough
//     to shard is past the point where an exact global scan is cheap).
//   - Neighbors — exact membership/ordering within the window; the absolute rank
//     numbers are approximate (anchored on the member's approximate rank).
type ShardedEngine struct {
	rdb    redis.UniversalClient
	re     *RedisEngine
	shards int
}

// NewShardedEngine builds an engine that shards every board into `shards`
// sorted sets. shards < 1 is treated as 1 (degenerates to a single set).
func NewShardedEngine(rdb redis.UniversalClient, shards int) *ShardedEngine {
	if shards < 1 {
		shards = 1
	}
	return &ShardedEngine{rdb: rdb, re: NewRedisEngine(rdb), shards: shards}
}

var _ RankingEngine = (*ShardedEngine)(nil)

// Shards reports the shard count.
func (s *ShardedEngine) Shards() int { return s.shards }

// shardOf maps a member to a shard via FNV-1a (stable across processes/restarts).
func (s *ShardedEngine) shardOf(member string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(member))
	return int(h.Sum32() % uint32(s.shards))
}

// shardBoard returns the physical sub-board for shard i. The Board name is
// suffixed so each shard is a distinct key (distinct hash slot on a cluster);
// the config is preserved so each shard maintains its own histogram.
func (s *ShardedEngine) shardBoard(b Board, i int) Board {
	k := b.Key
	k.Board = b.Key.Board + "#s" + strconv.Itoa(i)
	return Board{Key: k, Config: b.Config}
}

// --- writes: route to the member's shard ---

func (s *ShardedEngine) Submit(ctx context.Context, b Board, member string, score float64, t time.Time) (SubmitResult, error) {
	return s.re.Submit(ctx, s.shardBoard(b, s.shardOf(member)), member, score, t)
}

func (s *ShardedEngine) SubmitBatch(ctx context.Context, ops []SubmitOp) ([]SubmitResult, error) {
	routed := make([]SubmitOp, len(ops))
	for i, op := range ops {
		routed[i] = op
		routed[i].Board = s.shardBoard(op.Board, s.shardOf(op.Member))
	}
	return s.re.SubmitBatch(ctx, routed)
}

func (s *ShardedEngine) Remove(ctx context.Context, b Board, member string) error {
	return s.re.Remove(ctx, s.shardBoard(b, s.shardOf(member)), member)
}

func (s *ShardedEngine) RemoveFromAll(ctx context.Context, lb LogicalBoard, member string) error {
	sharded := lb
	sharded.Board = lb.Board + "#s" + strconv.Itoa(s.shardOf(member))
	return s.re.RemoveFromAll(ctx, sharded, member)
}

// Segments unions the live segment names across all shards of lb (each shard
// holds its own physical keys under the board#s<i> name).
func (s *ShardedEngine) Segments(ctx context.Context, lb LogicalBoard) ([]string, error) {
	seen := map[string]bool{}
	segs := []string{}
	for i := 0; i < s.shards; i++ {
		sharded := lb
		sharded.Board = lb.Board + "#s" + strconv.Itoa(i)
		part, err := s.re.Segments(ctx, sharded)
		if err != nil {
			return nil, err
		}
		for _, sg := range part {
			if !seen[sg] {
				seen[sg] = true
				segs = append(segs, sg)
			}
		}
	}
	sort.Strings(segs)
	return segs, nil
}

func (s *ShardedEngine) Reset(ctx context.Context, b Board) error {
	for i := 0; i < s.shards; i++ {
		if err := s.re.Reset(ctx, s.shardBoard(b, i)); err != nil {
			return err
		}
	}
	return nil
}

func (s *ShardedEngine) Count(ctx context.Context, b Board) (int64, error) {
	var total int64
	for i := 0; i < s.shards; i++ {
		n, err := s.re.Count(ctx, s.shardBoard(b, i))
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// --- candidate ordering ---

type shardCand struct {
	member string
	stored float64
}

// lessInOrder reports whether (am,as) ranks before (bm,bs) in the board's global
// order. It mirrors Redis range order: descending boards put higher scores first
// and break ties by member descending (ZREVRANGE); ascending boards invert both.
func lessInOrder(asc bool, am string, as float64, bm string, bs float64) bool {
	if as != bs {
		if asc {
			return as < bs
		}
		return as > bs
	}
	if asc {
		return am < bm
	}
	return am > bm
}

// shardRange fetches the top (stop+1) entries of one shard with stored scores.
func (s *ShardedEngine) shardRange(ctx context.Context, b Board, i int, stop int64) ([]shardCand, error) {
	key := s.shardBoard(b, i).Key.zKey()
	var zs []redis.Z
	var err error
	if b.Config.withDefaults().SortOrder == SortAsc {
		zs, err = s.rdb.ZRangeWithScores(ctx, key, 0, stop).Result()
	} else {
		zs, err = s.rdb.ZRevRangeWithScores(ctx, key, 0, stop).Result()
	}
	if err != nil {
		return nil, err
	}
	out := make([]shardCand, len(zs))
	for j, z := range zs {
		m, _ := z.Member.(string)
		out[j] = shardCand{member: m, stored: z.Score}
	}
	return out, nil
}

// rangeMerged returns the exact global ranking slice [start, stop] by merging the
// per-shard top-(stop+1) ranges. Exact because the global top-(stop+1) is a
// subset of the union of the per-shard top-(stop+1).
func (s *ShardedEngine) rangeMerged(ctx context.Context, b Board, start, stop int64) ([]RankEntry, error) {
	if err := b.validate(); err != nil {
		return nil, err
	}
	if stop < start || stop < 0 {
		return []RankEntry{}, nil
	}
	cfg := b.Config.withDefaults()
	asc := cfg.SortOrder == SortAsc
	all := make([]shardCand, 0, (stop+1)*int64(s.shards))
	for i := 0; i < s.shards; i++ {
		cands, err := s.shardRange(ctx, b, i, stop)
		if err != nil {
			return nil, err
		}
		all = append(all, cands...)
	}
	sort.Slice(all, func(i, j int) bool {
		return lessInOrder(asc, all[i].member, all[i].stored, all[j].member, all[j].stored)
	})
	if int64(len(all)) <= start {
		return []RankEntry{}, nil
	}
	end := stop + 1
	if end > int64(len(all)) {
		end = int64(len(all))
	}
	codec := newScoreCodec(cfg)
	out := make([]RankEntry, 0, end-start)
	for idx := start; idx < end; idx++ {
		c := all[idx]
		out = append(out, RankEntry{Member: c.member, Score: codec.decode(c.stored), Rank: idx + 1, Exact: true})
	}
	return out, nil
}

func (s *ShardedEngine) TopN(ctx context.Context, b Board, n int) ([]RankEntry, error) {
	if n <= 0 {
		return []RankEntry{}, nil
	}
	return s.rangeMerged(ctx, b, 0, int64(n-1))
}

func (s *ShardedEngine) Page(ctx context.Context, b Board, offset, limit int) ([]RankEntry, error) {
	if limit <= 0 || offset < 0 {
		return []RankEntry{}, nil
	}
	return s.rangeMerged(ctx, b, int64(offset), int64(offset+limit-1))
}

// --- rank (approximate, summed histograms) ---

func (s *ShardedEngine) GetRank(ctx context.Context, b Board, member string) (RankEntry, error) {
	return s.approxRank(ctx, b, member)
}

func (s *ShardedEngine) GetApproxRank(ctx context.Context, b Board, member string) (RankEntry, error) {
	return s.approxRank(ctx, b, member)
}

func (s *ShardedEngine) approxRank(ctx context.Context, b Board, member string) (RankEntry, error) {
	if err := b.validate(); err != nil {
		return RankEntry{}, err
	}
	cfg := b.Config.withDefaults()
	if !cfg.ApproxRank {
		return RankEntry{}, ErrApproxDisabled
	}
	sb := s.shardBoard(b, s.shardOf(member))
	stored, err := s.rdb.ZScore(ctx, sb.Key.zKey(), member).Result()
	if err == redis.Nil {
		return RankEntry{}, ErrMemberNotFound
	}
	if err != nil {
		return RankEntry{}, err
	}
	primary := newScoreCodec(cfg).decode(stored)

	// Read every shard's histogram in ONE pipeline (one round trip) rather than
	// a sequential HGETALL per shard, then sum the buckets. On a cluster the keys
	// span slots; go-redis splits the pipeline by node automatically.
	pipe := s.rdb.Pipeline()
	hgets := make([]*redis.MapStringStringCmd, s.shards)
	hists := make([]*Histogram, s.shards)
	for i := 0; i < s.shards; i++ {
		hists[i] = boardHistogram(s.rdb, s.shardBoard(b, i))
		hgets[i] = pipe.HGetAll(ctx, hists[i].key)
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return RankEntry{}, err
	}
	total := make([]int64, cfg.ApproxBuckets)
	for i := 0; i < s.shards; i++ {
		cs, err := hists[i].countsFrom(hgets[i].Val())
		if err != nil {
			return RankEntry{}, err
		}
		for j := range cs {
			total[j] += cs[j]
		}
	}
	idx := hists[0].bucketIndex(primary)
	rank := approxRankFromCounts(total, idx, cfg.SortOrder == SortAsc)
	return RankEntry{Member: member, Score: primary, Rank: rank, Exact: false}, nil
}

// --- friends (exact within the supplied set) ---

func (s *ShardedEngine) FriendRank(ctx context.Context, b Board, members []string) ([]RankEntry, error) {
	if err := b.validate(); err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return []RankEntry{}, nil
	}
	cfg := b.Config.withDefaults()
	pipe := s.rdb.Pipeline()
	cmds := make([]*redis.FloatCmd, len(members))
	for i, m := range members {
		cmds[i] = pipe.ZScore(ctx, s.shardBoard(b, s.shardOf(m)).Key.zKey(), m)
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, err
	}
	found := make([]shardCand, 0, len(members))
	for i, c := range cmds {
		v, err := c.Result()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			return nil, err
		}
		found = append(found, shardCand{members[i], v})
	}
	asc := cfg.SortOrder == SortAsc
	sort.SliceStable(found, func(i, j int) bool {
		return lessInOrder(asc, found[i].member, found[i].stored, found[j].member, found[j].stored)
	})
	codec := newScoreCodec(cfg)
	out := make([]RankEntry, len(found))
	for i, f := range found {
		out[i] = RankEntry{Member: f.member, Score: codec.decode(f.stored), Rank: int64(i + 1), Exact: true}
	}
	return out, nil
}

// --- neighbors (score-window scatter-gather) ---

func (s *ShardedEngine) Neighbors(ctx context.Context, b Board, member string, k int) ([]RankEntry, error) {
	if err := b.validate(); err != nil {
		return nil, err
	}
	if k < 0 {
		k = 0
	}
	cfg := b.Config.withDefaults()
	asc := cfg.SortOrder == SortAsc
	sb := s.shardBoard(b, s.shardOf(member))
	stored, err := s.rdb.ZScore(ctx, sb.Key.zKey(), member).Result()
	if err == redis.Nil {
		return nil, ErrMemberNotFound
	}
	if err != nil {
		return nil, err
	}
	sval := strconv.FormatFloat(stored, 'g', -1, 64)

	// From each shard pull the k+1 entries on each side of the member's score.
	// The union is guaranteed to contain the true k nearest neighbors on each
	// side, since at most k members globally sit between the member and its kth
	// neighbor, so no single shard contributes more than k of them.
	seen := make(map[string]struct{})
	cands := make([]shardCand, 0, (2*k+2)*s.shards)
	add := func(zs []redis.Z) {
		for _, z := range zs {
			m, _ := z.Member.(string)
			if _, dup := seen[m]; dup {
				continue
			}
			seen[m] = struct{}{}
			cands = append(cands, shardCand{member: m, stored: z.Score})
		}
	}
	count := int64(k + 1)
	for i := 0; i < s.shards; i++ {
		key := s.shardBoard(b, i).Key.zKey()
		geq, err := s.rdb.ZRangeByScoreWithScores(ctx, key, &redis.ZRangeBy{Min: sval, Max: "+inf", Offset: 0, Count: count}).Result()
		if err != nil {
			return nil, err
		}
		leq, err := s.rdb.ZRevRangeByScoreWithScores(ctx, key, &redis.ZRangeBy{Min: "-inf", Max: sval, Offset: 0, Count: count}).Result()
		if err != nil {
			return nil, err
		}
		add(geq)
		add(leq)
	}
	sort.Slice(cands, func(i, j int) bool {
		return lessInOrder(asc, cands[i].member, cands[i].stored, cands[j].member, cands[j].stored)
	})
	// Locate the member and slice +/- k around it.
	mi := -1
	for i, c := range cands {
		if c.member == member {
			mi = i
			break
		}
	}
	if mi < 0 {
		return nil, ErrMemberNotFound
	}
	lo := mi - k
	if lo < 0 {
		lo = 0
	}
	hi := mi + k
	if hi > len(cands)-1 {
		hi = len(cands) - 1
	}
	// Anchor absolute ranks on the member's approximate rank; relative ordering
	// is exact, so neighbor j gets base + (j - mi). Exact=false.
	base := int64(mi + 1)
	if me, err := s.approxRank(ctx, b, member); err == nil {
		base = me.Rank
	} else if err != ErrApproxDisabled {
		return nil, err
	}
	codec := newScoreCodec(cfg)
	out := make([]RankEntry, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		out = append(out, RankEntry{
			Member: cands[i].member,
			Score:  codec.decode(cands[i].stored),
			Rank:   base + int64(i-mi),
			Exact:  false,
		})
	}
	return out, nil
}
