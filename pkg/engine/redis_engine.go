package engine

import (
	"context"
	"sort"
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
	return results, nil
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
	return e.rdb.ZRem(ctx, b.Key.zKey(), member).Err()
}

// Reset deletes the board entirely (used for window rollover).
func (e *RedisEngine) Reset(ctx context.Context, b Board) error {
	if err := b.Key.validate(); err != nil {
		return err
	}
	return e.rdb.Del(ctx, b.Key.zKey(), b.Key.hKey(), b.Key.metaKey()).Err()
}
