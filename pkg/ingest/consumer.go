package ingest

import (
	"context"
	"sync"
	"time"

	"github.com/kodeni-am/leaderboard/pkg/engine"
)

// Consumer is the simple pull-based projector: it reads each partition after a
// per-partition cursor and applies records to the engine. It is used for the
// in-memory log, local/single-process runs, and Rebuild. The durable,
// horizontally-scaled live path on Redis is GroupConsumer.
type Consumer struct {
	log      Log
	resolver BoardResolver
	eng      engine.RankingEngine
	batch    int

	mu      sync.Mutex
	cursors map[int]string // partition -> last applied id
}

func NewConsumer(log Log, resolver BoardResolver, eng engine.RankingEngine) *Consumer {
	return &Consumer{log: log, resolver: resolver, eng: eng, batch: 256, cursors: map[int]string{}}
}

// recordToOps fans one record out to per-physical-board submit ops.
func recordToOps(lb engine.LogicalBoard, rec Record) []engine.SubmitOp {
	keys := engine.DerivePhysicalBoards(lb, engine.Event{
		Member:   rec.Member,
		Score:    rec.Score,
		Time:     rec.Time,
		Segments: rec.Segments,
	})
	ops := make([]engine.SubmitOp, len(keys))
	for i, k := range keys {
		ops[i] = engine.SubmitOp{
			Board:  engine.Board{Key: k, Config: lb.Config},
			Member: rec.Member,
			Score:  rec.Score,
			Time:   rec.Time,
		}
	}
	return ops
}

// applyRecords applies a mixed batch of submits and tombstones in log order:
// consecutive submits are batched into one SubmitBatch; a removal flushes the
// pending batch first (so earlier submits of the same member land before the
// removal), then removes the member from every live physical board. Records
// whose board is no longer registered are skipped.
func applyRecords(ctx context.Context, eng engine.RankingEngine, resolver BoardResolver, recs []Record) error {
	var ops []engine.SubmitOp
	flush := func() error {
		if len(ops) == 0 {
			return nil
		}
		_, err := eng.SubmitBatch(ctx, ops)
		ops = nil
		return err
	}
	for _, rec := range recs {
		lb, ok := resolver.Resolve(rec.App, rec.Board)
		if !ok {
			continue
		}
		if rec.Op == OpRemove {
			if err := flush(); err != nil {
				return err
			}
			if err := eng.RemoveFromAll(ctx, lb, rec.Member); err != nil {
				return err
			}
			continue
		}
		ops = append(ops, recordToOps(lb, rec)...)
	}
	return flush()
}

// Step reads and applies up to one batch per partition. It returns the total
// number of records processed (0 when every partition is drained).
func (c *Consumer) Step(ctx context.Context) (int, error) {
	total := 0
	for p := 0; p < c.log.Partitions(); p++ {
		c.mu.Lock()
		cur := c.cursors[p]
		c.mu.Unlock()

		recs, err := c.log.ReadPartition(ctx, p, cur, c.batch)
		if err != nil {
			return total, err
		}
		if len(recs) == 0 {
			continue
		}
		if err := applyRecords(ctx, c.eng, c.resolver, recs); err != nil {
			return total, err
		}
		c.mu.Lock()
		c.cursors[p] = recs[len(recs)-1].ID
		c.mu.Unlock()
		total += len(recs)
	}
	return total, nil
}

// Drain applies all currently-available records and returns when caught up.
func (c *Consumer) Drain(ctx context.Context) error {
	for {
		n, err := c.Step(ctx)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
	}
}

// Run continuously applies records, polling at the given interval when idle.
func (c *Consumer) Run(ctx context.Context, poll time.Duration) error {
	if poll <= 0 {
		poll = 50 * time.Millisecond
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := c.Step(ctx)
		if err != nil {
			return err
		}
		if n == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(poll):
			}
		}
	}
}

// Rebuild replays the entire log into eng, reconstructing ranking state. The
// target engine should be empty (best/increment replay is order-independent;
// last-wins relies on each partition's preserved order, and a member's events
// always share one partition).
func Rebuild(ctx context.Context, log Log, resolver BoardResolver, eng engine.RankingEngine) error {
	return NewConsumer(log, resolver, eng).Drain(ctx)
}
