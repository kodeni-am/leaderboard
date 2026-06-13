package ingest

import (
	"context"
	"fmt"
	"strconv"
	"sync"
)

// MemLog is an in-memory Log for tests and single-process local runs. It is
// ordered and append-only but not durable across restarts.
type MemLog struct {
	mu      sync.RWMutex
	records []Record
}

// NewMemLog returns an empty in-memory log.
func NewMemLog() *MemLog { return &MemLog{} }

func (l *MemLog) Append(_ context.Context, rec *Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Monotonic, zero-padded ids so lexical and numeric order agree.
	rec.ID = fmt.Sprintf("%012d", len(l.records)+1)
	cp := *rec
	l.records = append(l.records, cp)
	return nil
}

func (l *MemLog) Read(_ context.Context, after string, max int) ([]Record, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	start := 0
	if after != "" {
		n, err := strconv.Atoi(after)
		if err != nil {
			return nil, fmt.Errorf("memlog: bad cursor %q: %w", after, err)
		}
		start = n // ids are 1-based; index `after` == records[after:]
	}
	if start > len(l.records) {
		start = len(l.records)
	}
	end := len(l.records)
	if max > 0 && start+max < end {
		end = start + max
	}
	out := make([]Record, end-start)
	copy(out, l.records[start:end])
	return out, nil
}
