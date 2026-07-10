package engine

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisEngine is the sorted-set backed RankingEngine. It works against a single
// Redis node, ElastiCache/Valkey, or a Redis Cluster (board keys use hash tags
// so each board's keys co-locate on one slot).
type RedisEngine struct {
	rdb redis.UniversalClient
}

// NewRedisEngine wraps a go-redis universal client (single node or cluster).
func NewRedisEngine(rdb redis.UniversalClient) *RedisEngine {
	return &RedisEngine{rdb: rdb}
}

var _ RankingEngine = (*RedisEngine)(nil)

// submitCmds holds the pipelined futures for one queued submission so results
// can be resolved after Exec.
type submitCmds struct {
	policy UpdatePolicy
	codec  scoreCodec
	ch     *redis.IntCmd   // best/last: number of changed elements
	score  *redis.FloatCmd // best/last: authoritative current stored value
	inc    *redis.FloatCmd // increment: new score
	pre    *redis.FloatCmd // approx: stored value BEFORE this write (Nil if new)
}

func (sc submitCmds) result() (SubmitResult, error) {
	if sc.policy == UpdateIncrement {
		v, err := sc.inc.Result()
		if err != nil {
			return SubmitResult{}, err
		}
		return SubmitResult{Updated: true, Score: v}, nil
	}
	changed, err := sc.ch.Result()
	if err != nil {
		return SubmitResult{}, err
	}
	cur, err := sc.score.Result()
	if err != nil {
		return SubmitResult{}, err
	}
	return SubmitResult{Updated: changed > 0, Score: sc.codec.decode(cur)}, nil
}

func queueSubmit(ctx context.Context, pipe redis.Pipeliner, op SubmitOp, enc float64) submitCmds {
	cfg := op.Board.Config.withDefaults()
	key := op.Board.Key.zKey()
	sc := submitCmds{policy: cfg.UpdatePolicy, codec: newScoreCodec(cfg)}
	// For approx boards, capture the pre-write stored value so we can move the
	// member between histogram buckets. Pipelines preserve order, so this reads
	// the state before the ZADD/ZINCRBY queued below.
	if cfg.ApproxRank {
		sc.pre = pipe.ZScore(ctx, key, op.Member)
	}
	switch cfg.UpdatePolicy {
	case UpdateIncrement:
		sc.inc = pipe.ZIncrBy(ctx, key, op.Score, op.Member)
	default:
		args := redis.ZAddArgs{Ch: true, Members: []redis.Z{{Score: enc, Member: op.Member}}}
		if cfg.UpdatePolicy == UpdateBest {
			// GT/LT only restrict UPDATES to existing members; new members are
			// still added. This gives atomic best-score-wins with no read.
			if cfg.SortOrder == SortAsc {
				args.LT = true
			} else {
				args.GT = true
			}
		}
		sc.ch = pipe.ZAddArgs(ctx, key, args)
		sc.score = pipe.ZScore(ctx, key, op.Member)
	}
	return sc
}

// Submit writes a single score.
func (e *RedisEngine) Submit(ctx context.Context, b Board, member string, score float64, t time.Time) (SubmitResult, error) {
	res, err := e.SubmitBatch(ctx, []SubmitOp{{Board: b, Member: member, Score: score, Time: t}})
	if err != nil {
		return SubmitResult{}, err
	}
	return res[0], nil
}

// SubmitBatch pipelines many writes in one round trip. Used by the SP2 fan-out
// to apply a single score event to its N physical boards efficiently.
func (e *RedisEngine) SubmitBatch(ctx context.Context, ops []SubmitOp) ([]SubmitResult, error) {
	if len(ops) == 0 {
		return nil, nil
	}
	cmds := make([]submitCmds, len(ops))
	pipe := e.rdb.Pipeline()
	for i, op := range ops {
		if err := op.Board.validate(); err != nil {
			return nil, err
		}
		cfg := op.Board.Config.withDefaults()
		enc, err := newScoreCodec(cfg).encode(op.Score, op.Time)
		if err != nil {
			return nil, err
		}
		cmds[i] = queueSubmit(ctx, pipe, op, enc)
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, err
	}
	results := make([]SubmitResult, len(ops))
	for i := range cmds {
		r, err := cmds[i].result()
		if err != nil {
			return nil, err
		}
		results[i] = r
	}
	if err := e.maintainHistograms(ctx, ops, cmds, results); err != nil {
		return nil, err
	}
	return results, nil
}

// maintainHistograms moves each approx-board member between histogram buckets to
// reflect the write just applied, in a single follow-up pipeline. Accuracy
// assumes per-member writes are serialized (the ingest log partitions by member,
// so a member's events always flow through one consumer) — concurrent writes to
// the same member from different callers can drift the approximate counts.
func (e *RedisEngine) maintainHistograms(ctx context.Context, ops []SubmitOp, cmds []submitCmds, results []SubmitResult) error {
	pipe := e.rdb.Pipeline()
	queued := false
	for i, op := range ops {
		if cmds[i].pre == nil { // not an approx board
			continue
		}
		h := boardHistogram(e.rdb, op.Board)
		newPrimary := results[i].Score
		prev, err := cmds[i].pre.Result()
		if err == redis.Nil {
			// New member: count it once.
			h.QueueDelta(ctx, pipe, newPrimary, +1)
			queued = true
			continue
		}
		if err != nil {
			return err
		}
		oldPrimary := cmds[i].codec.decode(prev)
		if oldPrimary == newPrimary {
			continue // score unchanged (e.g. a non-improving `best` write)
		}
		h.QueueDelta(ctx, pipe, oldPrimary, -1)
		h.QueueDelta(ctx, pipe, newPrimary, +1)
		queued = true
	}
	if !queued {
		return nil
	}
	_, err := pipe.Exec(ctx)
	return err
}

// GetRank returns the member's exact 1-based rank and decoded score.
func (e *RedisEngine) GetRank(ctx context.Context, b Board, member string) (RankEntry, error) {
	if err := b.validate(); err != nil {
		return RankEntry{}, err
	}
	cfg := b.Config.withDefaults()
	key := b.Key.zKey()
	pipe := e.rdb.Pipeline()
	var rankCmd *redis.IntCmd
	if cfg.SortOrder == SortAsc {
		rankCmd = pipe.ZRank(ctx, key, member)
	} else {
		rankCmd = pipe.ZRevRank(ctx, key, member)
	}
	scoreCmd := pipe.ZScore(ctx, key, member)
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return RankEntry{}, err
	}
	rank, err := rankCmd.Result()
	if err == redis.Nil {
		return RankEntry{}, ErrMemberNotFound
	}
	if err != nil {
		return RankEntry{}, err
	}
	sc, err := scoreCmd.Result()
	if err == redis.Nil {
		return RankEntry{}, ErrMemberNotFound
	}
	if err != nil {
		return RankEntry{}, err
	}
	return RankEntry{
		Member: member,
		Score:  newScoreCodec(cfg).decode(sc),
		Rank:   rank + 1,
		Exact:  true,
	}, nil
}

// GetApproxRank estimates the member's global rank from the board's score
// histogram in O(buckets), without a ZRANK. The returned entry has Exact=false
// and a Rank accurate to one bucket width. It exists so the sharded engine can
// answer global rank without scatter-gathering every shard; on a single set,
// prefer GetRank (exact and already O(log N)). Returns ErrApproxDisabled if the
// board does not have ApproxRank configured.
func (e *RedisEngine) GetApproxRank(ctx context.Context, b Board, member string) (RankEntry, error) {
	if err := b.validate(); err != nil {
		return RankEntry{}, err
	}
	cfg := b.Config.withDefaults()
	if !cfg.ApproxRank {
		return RankEntry{}, ErrApproxDisabled
	}
	stored, err := e.rdb.ZScore(ctx, b.Key.zKey(), member).Result()
	if err == redis.Nil {
		return RankEntry{}, ErrMemberNotFound
	}
	if err != nil {
		return RankEntry{}, err
	}
	primary := newScoreCodec(cfg).decode(stored)
	h := boardHistogram(e.rdb, b)
	var rank int64
	if cfg.SortOrder == SortAsc {
		rank, err = h.ApproxRankAsc(ctx, primary)
	} else {
		rank, err = h.ApproxRankDesc(ctx, primary)
	}
	if err != nil {
		return RankEntry{}, err
	}
	return RankEntry{Member: member, Score: primary, Rank: rank, Exact: false}, nil
}

// rangeByRank returns entries for the inclusive rank window [start, stop].
func (e *RedisEngine) rangeByRank(ctx context.Context, b Board, start, stop int64) ([]RankEntry, error) {
	if err := b.validate(); err != nil {
		return nil, err
	}
	if stop < start {
		return []RankEntry{}, nil
	}
	cfg := b.Config.withDefaults()
	key := b.Key.zKey()
	var zs []redis.Z
	var err error
	if cfg.SortOrder == SortAsc {
		zs, err = e.rdb.ZRangeWithScores(ctx, key, start, stop).Result()
	} else {
		zs, err = e.rdb.ZRevRangeWithScores(ctx, key, start, stop).Result()
	}
	if err != nil {
		return nil, err
	}
	codec := newScoreCodec(cfg)
	out := make([]RankEntry, len(zs))
	for i, z := range zs {
		m, _ := z.Member.(string)
		out[i] = RankEntry{
			Member: m,
			Score:  codec.decode(z.Score),
			Rank:   start + int64(i) + 1,
			Exact:  true,
		}
	}
	return out, nil
}

// TopN returns the top n members.
func (e *RedisEngine) TopN(ctx context.Context, b Board, n int) ([]RankEntry, error) {
	if n <= 0 {
		return []RankEntry{}, nil
	}
	return e.rangeByRank(ctx, b, 0, int64(n-1))
}

// Page returns a slice of the ranking starting at offset (0-based).
func (e *RedisEngine) Page(ctx context.Context, b Board, offset, limit int) ([]RankEntry, error) {
	if limit <= 0 || offset < 0 {
		return []RankEntry{}, nil
	}
	return e.rangeByRank(ctx, b, int64(offset), int64(offset+limit-1))
}

// Neighbors returns the member plus up to k members on each side of it.
func (e *RedisEngine) Neighbors(ctx context.Context, b Board, member string, k int) ([]RankEntry, error) {
	if err := b.validate(); err != nil {
		return nil, err
	}
	if k < 0 {
		k = 0
	}
	cfg := b.Config.withDefaults()
	key := b.Key.zKey()
	var rank int64
	var err error
	if cfg.SortOrder == SortAsc {
		rank, err = e.rdb.ZRank(ctx, key, member).Result()
	} else {
		rank, err = e.rdb.ZRevRank(ctx, key, member).Result()
	}
	if err == redis.Nil {
		return nil, ErrMemberNotFound
	}
	if err != nil {
		return nil, err
	}
	start := rank - int64(k)
	if start < 0 {
		start = 0
	}
	return e.rangeByRank(ctx, b, start, rank+int64(k))
}

// FriendRank ranks an explicit set of members against each other (a friend
// leaderboard). Members with no entry on the board are omitted. Rank is the
// 1-based position within the supplied group.
func (e *RedisEngine) FriendRank(ctx context.Context, b Board, members []string) ([]RankEntry, error) {
	if err := b.validate(); err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return []RankEntry{}, nil
	}
	cfg := b.Config.withDefaults()
	key := b.Key.zKey()
	pipe := e.rdb.Pipeline()
	cmds := make([]*redis.FloatCmd, len(members))
	for i, m := range members {
		cmds[i] = pipe.ZScore(ctx, key, m)
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, err
	}
	type entry struct {
		member string
		stored float64
	}
	found := make([]entry, 0, len(members))
	for i, c := range cmds {
		v, err := c.Result()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			return nil, err
		}
		found = append(found, entry{members[i], v})
	}
	asc := cfg.SortOrder == SortAsc
	// Tie order must match the sorted-set range order so FriendRank agrees with
	// TopN/GetRank: ZRANGE (asc board) breaks score ties by member ascending;
	// ZREVRANGE (desc board) reverses that to member descending.
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].stored != found[j].stored {
			if asc {
				return found[i].stored < found[j].stored
			}
			return found[i].stored > found[j].stored
		}
		if asc {
			return found[i].member < found[j].member
		}
		return found[i].member > found[j].member
	})
	codec := newScoreCodec(cfg)
	out := make([]RankEntry, len(found))
	for i, f := range found {
		out[i] = RankEntry{Member: f.member, Score: codec.decode(f.stored), Rank: int64(i + 1), Exact: true}
	}
	return out, nil
}

// Count returns the number of members on the board.
func (e *RedisEngine) Count(ctx context.Context, b Board) (int64, error) {
	if err := b.Key.validate(); err != nil {
		return 0, err
	}
	return e.rdb.ZCard(ctx, b.Key.zKey()).Result()
}

// Remove deletes a member from the board.
func (e *RedisEngine) Remove(ctx context.Context, b Board, member string) error {
	if err := b.Key.validate(); err != nil {
		return err
	}
	cfg := b.Config.withDefaults()
	if !cfg.ApproxRank {
		return e.rdb.ZRem(ctx, b.Key.zKey(), member).Err()
	}
	// Approx board: decrement the member's histogram bucket if it was present.
	// ZScore before ZRem (pipeline order) gives the value being removed.
	pipe := e.rdb.Pipeline()
	scoreCmd := pipe.ZScore(ctx, b.Key.zKey(), member)
	remCmd := pipe.ZRem(ctx, b.Key.zKey(), member)
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return err
	}
	removed, err := remCmd.Result()
	if err != nil {
		return err
	}
	stored, err := scoreCmd.Result()
	if err == redis.Nil || removed == 0 {
		return nil // member wasn't on the board; nothing to decrement
	}
	if err != nil {
		return err
	}
	primary := newScoreCodec(cfg).decode(stored)
	return boardHistogram(e.rdb, b).Remove(ctx, primary)
}

// globEscape escapes Redis glob metacharacters so s matches literally when
// embedded in a SCAN MATCH pattern (board names may contain *?[] etc. —
// key validation only rejects ':', braces, and whitespace).
func globEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '*', '?', '[', ']', '^', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// scanBoardKeys returns the BoardKey of every live sorted set belonging to
// (app, board) — one per segment/window combination. Key components cannot
// contain ':' (validated on write), so splitting the hash tag is unambiguous.
// Like the window reaper's sweep, SCAN has single-node scope on Redis Cluster.
func scanBoardKeys(ctx context.Context, rdb redis.UniversalClient, app, board string) ([]BoardKey, error) {
	pattern := "lb:{" + globEscape(app) + ":" + globEscape(board) + ":*}:z"
	var keys []BoardKey
	var cursor uint64
	for {
		batch, next, err := rdb.Scan(ctx, cursor, pattern, 200).Result()
		if err != nil {
			return nil, err
		}
		for _, raw := range batch {
			open := strings.IndexByte(raw, '{')
			close := strings.IndexByte(raw, '}')
			if open < 0 || close < open {
				continue
			}
			parts := strings.Split(raw[open+1:close], ":")
			if len(parts) != 4 {
				continue
			}
			keys = append(keys, BoardKey{App: parts[0], Board: parts[1], Segment: parts[2], Window: parts[3]})
		}
		cursor = next
		if cursor == 0 {
			return keys, nil
		}
	}
}

// RemoveFromAll removes member from every live physical board of lb.
func (e *RedisEngine) RemoveFromAll(ctx context.Context, lb LogicalBoard, member string) error {
	keys, err := scanBoardKeys(ctx, e.rdb, lb.App, lb.Board)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if err := e.Remove(ctx, Board{Key: k, Config: lb.Config}, member); err != nil {
			return err
		}
	}
	return nil
}

// Reset deletes the board entirely (used for window rollover).
func (e *RedisEngine) Reset(ctx context.Context, b Board) error {
	if err := b.Key.validate(); err != nil {
		return err
	}
	return e.rdb.Del(ctx, b.Key.zKey(), b.Key.hKey(), b.Key.metaKey()).Err()
}
