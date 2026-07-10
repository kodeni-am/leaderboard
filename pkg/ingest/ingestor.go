package ingest

import (
	"context"
	"time"
)

// Ingestor is the synchronous front of the write path. Submit validates the
// board, applies idempotency, and durably appends to the log — then returns.
// Applying the score to the ranking tier happens asynchronously in Consumer,
// so the ack latency is one log append, and write bursts are absorbed by the
// log rather than the engine.
type Ingestor struct {
	log      Log
	resolver BoardResolver
	deduper  Deduper
	dedupTTL time.Duration
}

func NewIngestor(log Log, resolver BoardResolver, deduper Deduper) *Ingestor {
	if deduper == nil {
		deduper = NoopDeduper{}
	}
	return &Ingestor{log: log, resolver: resolver, deduper: deduper, dedupTTL: 24 * time.Hour}
}

// Submit durably records a score event. It returns accepted=false (no error)
// when the event was a duplicate of an already-seen idempotency key.
func (i *Ingestor) Submit(ctx context.Context, rec Record) (accepted bool, err error) {
	if _, ok := i.resolver.Resolve(rec.App, rec.Board); !ok {
		return false, ErrUnknownBoard
	}
	if rec.Time.IsZero() {
		rec.Time = time.Now().UTC()
	}
	if rec.Idem != "" {
		seen, err := i.deduper.SeenOrMark(ctx, rec.App+":"+rec.Idem, i.dedupTTL)
		if err != nil {
			return false, err
		}
		if seen {
			return false, nil
		}
	}
	if err := i.log.Append(ctx, &rec); err != nil {
		// Roll back the dedup reservation so a retry can succeed.
		if rec.Idem != "" {
			_ = i.deduper.Unmark(ctx, rec.App+":"+rec.Idem)
		}
		return false, err
	}
	return true, nil
}

// Remove durably appends a removal tombstone for (rec.Board, rec.Member).
// It partitions like the member's submits on that board, so consumers and
// rebuild apply it in order relative to them. Applying the removal to the
// ranking tier happens asynchronously in the consumer; callers wanting
// read-your-writes additionally apply it synchronously (removal is
// idempotent, so double application is harmless).
func (i *Ingestor) Remove(ctx context.Context, rec Record) error {
	if _, ok := i.resolver.Resolve(rec.App, rec.Board); !ok {
		return ErrUnknownBoard
	}
	rec.Op = OpRemove
	if rec.Time.IsZero() {
		rec.Time = time.Now().UTC()
	}
	return i.log.Append(ctx, &rec)
}
