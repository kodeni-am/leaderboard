// Package ingest is the SP2 write path: a durable append log in front of the
// ranking engine. The submit API appends a score event to the log and acks;
// a consumer asynchronously fans the event out to its physical boards and
// applies it to the engine. The log is the source of truth, so the Redis
// ranking tier is a rebuildable cache and write bursts are absorbed by the log
// rather than hammering Redis synchronously.
//
// The log is PARTITIONED by (app, board, member): all events for one member on
// one board land in the same partition, so they are consumed in order. This
// lets multiple workers consume different partitions in parallel (throughput)
// while preserving the per-member ordering that last-write-wins depends on.
package ingest

import (
	"context"
	"hash/fnv"
	"time"
)

// Record op types. The zero value is a score submit, so every pre-existing
// log entry (which has no op field) decodes as a submit.
const (
	OpSubmit = ""       // score submission (default)
	OpRemove = "remove" // tombstone: remove Member's entry from Board everywhere
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
	// Op discriminates the record type: OpSubmit ("") or OpRemove. Tombstones
	// use App/Board/Member; Score and Segments are meaningless on them.
	Op string `json:"op,omitempty"`
}

// Log is the durable, ordered, append-only, partitioned event log.
// Implementations: an in-memory log (tests/local, 1 partition) and a Redis
// Streams log (one stream per partition). Kinesis is a future seam.
type Log interface {
	// Append stores rec (routing it to a partition) and sets rec.ID.
	Append(ctx context.Context, rec *Record) error
	// Partitions is the number of partitions in the log (>=1).
	Partitions() int
	// ReadPartition returns up to max records in partition p whose ID sorts
	// strictly after `after` ("" = from the beginning), in append order. Used
	// for rebuild/replay; live consumption uses GroupConsumer.
	ReadPartition(ctx context.Context, p int, after string, max int) ([]Record, error)
}

// partitionOf routes a record to a partition by hashing (app, board, member).
func partitionOf(app, board, member string, partitions int) int {
	if partitions <= 1 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(app))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(board))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(member))
	return int(h.Sum32() % uint32(partitions))
}

// OwnedPartitions returns the partitions a worker owns under static assignment:
// partition p is owned by worker (p % workerCount). With workerCount<=1 a single
// worker owns all partitions.
func OwnedPartitions(partitions, workerIndex, workerCount int) []int {
	if workerCount <= 1 {
		owned := make([]int, partitions)
		for i := range owned {
			owned[i] = i
		}
		return owned
	}
	var owned []int
	for p := 0; p < partitions; p++ {
		if p%workerCount == workerIndex {
			owned = append(owned, p)
		}
	}
	return owned
}
