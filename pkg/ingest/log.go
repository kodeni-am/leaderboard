// Package ingest is the SP2 write path: a durable append log in front of the
// ranking engine. The submit API appends a score event to the log and acks;
// a consumer asynchronously fans the event out to its physical boards and
// applies it to the engine. The log is the source of truth, so the Redis
// ranking tier is a rebuildable cache and write bursts are absorbed by the log
// rather than hammering Redis synchronously.
package ingest

import (
	"context"
	"time"
)

// Record is one score event as stored in the durable log.
type Record struct {
	ID       string    `json:"id"` // assigned by the log; acts as the read cursor
	App      string    `json:"app"`
	Board    string    `json:"board"`
	Member   string    `json:"member"`
	Score    float64   `json:"score"`
	Time     time.Time `json:"time"`
	Segments []string  `json:"segments,omitempty"`
	Idem     string    `json:"idem,omitempty"` // client dedup key (optional)
}

// Log is the durable, ordered, append-only event log. Implementations: an
// in-memory log (tests/local), a Redis Streams log (single-node/self-host),
// and — as a future seam — Kinesis (the KinesisLog stub documents the contract).
type Log interface {
	// Append stores rec and sets rec.ID to the assigned cursor.
	Append(ctx context.Context, rec *Record) error
	// Read returns up to max records whose ID sorts strictly after `after`
	// ("" means from the beginning), in append order.
	Read(ctx context.Context, after string, max int) ([]Record, error)
}
