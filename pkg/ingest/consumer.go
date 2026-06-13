package ingest

import (
	"context"
	"sync"
	"time"

	"github.com/araasr/leaderboard/pkg/engine"
)

// Consumer reads records from the log and applies them to the ranking engine,
// fanning each event out to its physical boards. It is the only writer to the
// engine, which keeps the ranking tier a deterministic projection of the log.
type Consumer struct {
	log      Log
	resolver BoardResolver
	eng      engine.RankingEngine
	batch    int

	mu     sync.Mutex
	cursor string
}

func NewConsumer(log Log, resolver BoardResolver, eng engine.RankingEngine) *Consumer {
	return &Consumer{log: log, resolver: resolver, eng: eng, batch: 256}
}

// Cursor returns the id of the last applied record.
func (c *Consumer) Cursor() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cursor
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

// Step reads and applies the next batch. It returns the number of records
// processed (0 when the log is drained).
func (c *Consumer) Step(ctx context.Context) (int, error) {
	c.mu.Lock()
	cursor := c.cursor
	c.mu.Unlock()

	recs, err := c.log.Read(ctx, cursor, c.batch)
	if err != nil {
		return 0, err
	}
	if len(recs) == 0 {
		return 0, nil
	}
	var ops []engine.SubmitOp
	for _, rec := range recs {
		lb, ok := c.resolver.Resolve(rec.App, rec.Board)
		if !ok {
			// Board was deregistered after the event was logged; skip it but
			// still advance past it.
			continue
		}
		ops = append(ops, recordToOps(lb, rec)...)
	}
	if len(ops) > 0 {
		if _, err := c.eng.SubmitBatch(ctx, ops); err != nil {
			return 0, err
		}
	}
	c.mu.Lock()
	c.cursor = recs[len(recs)-1].ID
	c.mu.Unlock()
	return len(recs), nil
}

// Drain applies all currently-available records and returns when the log is
// caught up.
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
// It returns when ctx is cancelled.
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

// Rebuild replays the entire log from the beginning into eng, reconstructing
// the ranking state. The target engine should be empty (best/increment replay
// is order-independent; last-wins relies on the log's preserved order).
func Rebuild(ctx context.Context, log Log, resolver BoardResolver, eng engine.RankingEngine) error {
	c := NewConsumer(log, resolver, eng)
	return c.Drain(ctx)
}
