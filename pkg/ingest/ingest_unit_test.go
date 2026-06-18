package ingest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kodeni-am/leaderboard/pkg/engine"
)

func testRegistry() *StaticRegistry {
	r := NewStaticRegistry()
	r.Register(engine.LogicalBoard{App: "g", Board: "score"})
	return r
}

func TestMemLogAppendReadCursor(t *testing.T) {
	ctx := context.Background()
	l := NewMemLog()
	for i := 0; i < 5; i++ {
		rec := Record{App: "g", Board: "score", Member: "p", Score: float64(i)}
		if err := l.Append(ctx, &rec); err != nil {
			t.Fatal(err)
		}
		if rec.ID == "" {
			t.Fatal("append did not set ID")
		}
	}
	all, _ := l.ReadPartition(ctx, 0, "", 0)
	if len(all) != 5 {
		t.Fatalf("read all = %d, want 5", len(all))
	}
	// Cursor read: after the 2nd record -> 3 remaining.
	rest, _ := l.ReadPartition(ctx, 0, all[1].ID, 0)
	if len(rest) != 3 || rest[0].Score != 2 {
		t.Fatalf("cursor read wrong: %d records, first score %v", len(rest), rest[0].Score)
	}
	// Max bounding.
	two, _ := l.ReadPartition(ctx, 0, "", 2)
	if len(two) != 2 {
		t.Fatalf("max read = %d, want 2", len(two))
	}
}

func TestMemDeduper(t *testing.T) {
	ctx := context.Background()
	d := NewMemDeduper()
	seen, _ := d.SeenOrMark(ctx, "k1", time.Minute)
	if seen {
		t.Error("first SeenOrMark should be not-seen")
	}
	seen, _ = d.SeenOrMark(ctx, "k1", time.Minute)
	if !seen {
		t.Error("second SeenOrMark should be seen")
	}
	_ = d.Unmark(ctx, "k1")
	seen, _ = d.SeenOrMark(ctx, "k1", time.Minute)
	if seen {
		t.Error("after Unmark should be not-seen again")
	}
}

func TestIngestorAcceptAndDedup(t *testing.T) {
	ctx := context.Background()
	log := NewMemLog()
	ing := NewIngestor(log, testRegistry(), NewMemDeduper())

	rec := Record{App: "g", Board: "score", Member: "p", Score: 10, Idem: "abc"}
	acc, err := ing.Submit(ctx, rec)
	if err != nil || !acc {
		t.Fatalf("first submit: acc=%v err=%v", acc, err)
	}
	// Same idem key -> rejected as duplicate, no error.
	acc, err = ing.Submit(ctx, rec)
	if err != nil || acc {
		t.Fatalf("dup submit: acc=%v err=%v", acc, err)
	}
	all, _ := log.ReadPartition(ctx, 0, "", 0)
	if len(all) != 1 {
		t.Fatalf("dup was appended: %d records", len(all))
	}
}

func TestIngestorUnknownBoard(t *testing.T) {
	ing := NewIngestor(NewMemLog(), testRegistry(), NewMemDeduper())
	_, err := ing.Submit(context.Background(), Record{App: "g", Board: "ghost", Member: "p", Score: 1})
	if !errors.Is(err, ErrUnknownBoard) {
		t.Errorf("expected ErrUnknownBoard, got %v", err)
	}
}

// failingLog always errors on Append, to verify dedup rollback.
type failingLog struct{}

func (failingLog) Append(context.Context, *Record) error { return errors.New("boom") }
func (failingLog) Partitions() int                       { return 1 }
func (failingLog) ReadPartition(context.Context, int, string, int) ([]Record, error) {
	return nil, nil
}

func TestIngestorUnmarksOnAppendFailure(t *testing.T) {
	ctx := context.Background()
	d := NewMemDeduper()
	ing := NewIngestor(failingLog{}, testRegistry(), d)
	rec := Record{App: "g", Board: "score", Member: "p", Score: 1, Idem: "retry-me"}
	if _, err := ing.Submit(ctx, rec); err == nil {
		t.Fatal("expected append error")
	}
	// The idem key must have been rolled back so a retry isn't silently dropped.
	if seen, _ := d.SeenOrMark(ctx, "g:retry-me", time.Minute); seen {
		t.Error("idem key was not rolled back after append failure")
	}
}
