package ingest

import (
	"context"
	"strings"
	"time"

	"github.com/kodeni-am/leaderboard/pkg/engine"
	"github.com/redis/go-redis/v9"
)

// GroupConsumer is the durable, horizontally-scalable live consumption path. It
// reads its owned partitions via Redis Streams consumer groups (XREADGROUP),
// applies records to the engine, and XACKs them. The group's offset is durable
// in Redis, so a restart resumes from un-acked entries instead of replaying the
// whole log. Crashed workers' un-acked entries are reclaimed via XAUTOCLAIM.
//
// Delivery is at-least-once. Apply is made idempotent by marking each stream
// entry id (SET NX) AFTER it is applied and skipping already-applied ids on
// redelivery. This makes best/last exactly-once; the residual is that an
// `increment` board can double-count entries if a batch is partially applied
// but not yet marked — a worker crash between apply and mark, or a mid-batch
// error after applyRecords has flushed some submits (documented; rare).
type GroupConsumer struct {
	log          *RedisLog
	rdb          redis.UniversalClient
	resolver     BoardResolver
	eng          engine.RankingEngine
	group        string
	consumer     string
	owned        []int
	batch        int64
	block        time.Duration
	appliedTTL   time.Duration
	claimMinIdle time.Duration
	onConsumed   func(int) // optional metrics hook, called with records handled
}

// GroupOptions configures a GroupConsumer.
type GroupOptions struct {
	Group        string        // consumer group name (default "rankers")
	Consumer     string        // this worker's consumer name (default "c-0")
	WorkerIndex  int           // this worker's index for static partition ownership
	WorkerCount  int           // total workers (default 1 = own all partitions)
	Batch        int64         // max entries per read (default 256)
	Block        time.Duration // XREADGROUP block time (default 2s)
	AppliedTTL   time.Duration // TTL of idempotency markers (default 24h)
	ClaimMinIdle time.Duration // min idle before reclaiming pending (default 30s)
	OnConsumed   func(int)     // optional: called with the number of records handled
}

func NewGroupConsumer(log *RedisLog, resolver BoardResolver, eng engine.RankingEngine, opts GroupOptions) *GroupConsumer {
	if opts.Group == "" {
		opts.Group = "rankers"
	}
	if opts.Consumer == "" {
		opts.Consumer = "c-0"
	}
	if opts.Batch <= 0 {
		opts.Batch = 256
	}
	if opts.Block <= 0 {
		opts.Block = 2 * time.Second
	}
	if opts.AppliedTTL <= 0 {
		opts.AppliedTTL = 24 * time.Hour
	}
	if opts.ClaimMinIdle <= 0 {
		opts.ClaimMinIdle = 30 * time.Second
	}
	return &GroupConsumer{
		log:          log,
		rdb:          log.rdb,
		resolver:     resolver,
		eng:          eng,
		group:        opts.Group,
		consumer:     opts.Consumer,
		owned:        OwnedPartitions(log.Partitions(), opts.WorkerIndex, opts.WorkerCount),
		batch:        opts.Batch,
		block:        opts.Block,
		appliedTTL:   opts.AppliedTTL,
		claimMinIdle: opts.ClaimMinIdle,
		onConsumed:   opts.OnConsumed,
	}
}

// Owned returns the partitions this consumer is responsible for.
func (g *GroupConsumer) Owned() []int { return g.owned }

func (g *GroupConsumer) appliedKey(stream, id string) string {
	return "lb:applied:" + stream + "-" + id
}

// isNoGroup reports whether err is Redis's NOGROUP (stream/group missing).
func isNoGroup(err error) bool {
	return err != nil && strings.Contains(err.Error(), "NOGROUP")
}

// EnsureGroups creates the consumer group on each owned partition's stream,
// creating the stream if necessary (MKSTREAM). Idempotent.
func (g *GroupConsumer) EnsureGroups(ctx context.Context) error {
	for _, p := range g.owned {
		err := g.rdb.XGroupCreateMkStream(ctx, g.log.StreamName(p), g.group, "0").Err()
		if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
			return err
		}
	}
	return nil
}

// apply processes a batch of messages from one stream idempotently and ACKs
// them. Returns the number of messages handled.
func (g *GroupConsumer) apply(ctx context.Context, stream string, msgs []redis.XMessage) (int, error) {
	if len(msgs) == 0 {
		return 0, nil
	}
	// 1. Check which entries were already applied (idempotency).
	pipe := g.rdb.Pipeline()
	existsCmds := make([]*redis.IntCmd, len(msgs))
	for i, m := range msgs {
		existsCmds[i] = pipe.Exists(ctx, g.appliedKey(stream, m.ID))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return 0, err
	}

	// 2. Collect not-yet-applied, resolvable records (in stream order).
	var recs []Record
	var newIDs []string
	allIDs := make([]string, len(msgs))
	for i, m := range msgs {
		allIDs[i] = m.ID
		if existsCmds[i].Val() > 0 {
			continue // already applied: skip, will still ACK
		}
		rec, ok, err := messageToRecord(m)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue // malformed: skip, will ACK
		}
		if _, ok := g.resolver.Resolve(rec.App, rec.Board); ok {
			recs = append(recs, rec)
			newIDs = append(newIDs, m.ID)
		}
	}

	// 3. Apply in order, then 4. mark applied (apply-before-mark = at-least-once).
	if err := applyRecords(ctx, g.eng, g.resolver, recs); err != nil {
		return 0, err
	}
	if len(newIDs) > 0 {
		mp := g.rdb.Pipeline()
		for _, id := range newIDs {
			mp.Set(ctx, g.appliedKey(stream, id), 1, g.appliedTTL)
		}
		if _, err := mp.Exec(ctx); err != nil {
			return 0, err
		}
	}

	// 5. ACK everything we've handled.
	if err := g.rdb.XAck(ctx, stream, g.group, allIDs...).Err(); err != nil {
		return 0, err
	}
	if g.onConsumed != nil {
		g.onConsumed(len(msgs))
	}
	return len(msgs), nil
}

// Step reads new entries from all owned partitions (one blocking XREADGROUP),
// applies and ACKs them. Returns the number of records processed.
func (g *GroupConsumer) Step(ctx context.Context) (int, error) {
	total := 0
	// Read each owned partition's stream individually (non-blocking). One stream
	// per XREADGROUP keeps the command to a single key/slot, which is required on
	// Redis Cluster (a multi-stream read spans slots → CROSSSLOT). Run() polls
	// when idle, so we don't need server-side BLOCK here.
	for _, p := range g.owned {
		stream := g.log.StreamName(p)
		res, err := g.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    g.group,
			Consumer: g.consumer,
			Streams:  []string{stream, ">"},
			Count:    g.batch,
			Block:    -1, // non-blocking
		}).Result()
		if err == redis.Nil {
			continue
		}
		if isNoGroup(err) {
			// Stream/group vanished (Redis flush/restart, or a fresh partition).
			if e := g.EnsureGroups(ctx); e != nil {
				return total, e
			}
			continue
		}
		if err != nil {
			return total, err
		}
		for _, st := range res {
			n, err := g.apply(ctx, st.Stream, st.Messages)
			if err != nil {
				return total, err
			}
			total += n
		}
	}
	return total, nil
}

// Reclaim picks up entries that have been pending (delivered but un-acked)
// longer than ClaimMinIdle — i.e. left behind by crashed workers — and applies
// them. Returns the number reclaimed.
func (g *GroupConsumer) Reclaim(ctx context.Context) (int, error) {
	total := 0
	for _, p := range g.owned {
		stream := g.log.StreamName(p)
		start := "0-0"
		for {
			msgs, next, err := g.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
				Stream:   stream,
				Group:    g.group,
				Consumer: g.consumer,
				MinIdle:  g.claimMinIdle,
				Start:    start,
				Count:    g.batch,
			}).Result()
			if isNoGroup(err) {
				return total, g.EnsureGroups(ctx)
			}
			if err != nil {
				return total, err
			}
			if len(msgs) > 0 {
				n, err := g.apply(ctx, stream, msgs)
				if err != nil {
					return total, err
				}
				total += n
			}
			if next == "0-0" || next == "" {
				break
			}
			start = next
		}
	}
	return total, nil
}

// Run drives the consumer until ctx is cancelled, periodically reclaiming
// pending entries from crashed workers.
func (g *GroupConsumer) Run(ctx context.Context, claimInterval time.Duration) error {
	if err := g.EnsureGroups(ctx); err != nil {
		return err
	}
	if claimInterval <= 0 {
		claimInterval = 30 * time.Second
	}
	ticker := time.NewTicker(claimInterval)
	defer ticker.Stop()
	poll := g.block // reuse the configured interval as the idle poll period
	if poll <= 0 {
		poll = 250 * time.Millisecond
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, err := g.Step(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := g.Reclaim(ctx); err != nil && ctx.Err() == nil {
				return err
			}
		default:
		}
		// Step is non-blocking; sleep briefly when there was nothing to do so we
		// don't busy-loop. New entries are picked up within `poll`.
		if n == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(poll):
			}
		}
	}
}
