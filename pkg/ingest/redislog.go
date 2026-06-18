package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"
)

// RedisLog is a durable, partitioned Log backed by Redis Streams. Each
// partition is its own stream ("<prefix>:<p>"), so append throughput and
// consumption parallelism scale with the partition count. Suitable for
// single-node and self-hosted deployments.
type RedisLog struct {
	rdb        redis.UniversalClient
	prefix     string
	partitions int
	maxLen     int64 // optional approximate per-stream cap (0 = unbounded)
}

// NewRedisLog creates a partitioned stream log. partitions<1 defaults to 1.
// maxLen>0 trims each stream to roughly that many entries (XADD MAXLEN ~); use
// 0 to retain everything (required if the log is the sole rebuild source).
func NewRedisLog(rdb redis.UniversalClient, prefix string, partitions int, maxLen int64) *RedisLog {
	if prefix == "" {
		prefix = "lb:ingest"
	}
	if partitions < 1 {
		partitions = 1
	}
	return &RedisLog{rdb: rdb, prefix: prefix, partitions: partitions, maxLen: maxLen}
}

// Partitions returns the partition count.
func (l *RedisLog) Partitions() int { return l.partitions }

// StreamName returns the Redis stream key for partition p.
func (l *RedisLog) StreamName(p int) string {
	return l.prefix + ":" + strconv.Itoa(p)
}

func (l *RedisLog) Append(ctx context.Context, rec *Record) error {
	payload, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("redislog: marshal: %w", err)
	}
	p := partitionOf(rec.App, rec.Board, rec.Member, l.partitions)
	args := &redis.XAddArgs{
		Stream: l.StreamName(p),
		Values: map[string]any{"d": payload},
	}
	if l.maxLen > 0 {
		args.MaxLen = l.maxLen
		args.Approx = true
	}
	id, err := l.rdb.XAdd(ctx, args).Result()
	if err != nil {
		return fmt.Errorf("redislog: xadd: %w", err)
	}
	rec.ID = id
	return nil
}

func (l *RedisLog) ReadPartition(ctx context.Context, p int, after string, max int) ([]Record, error) {
	if p < 0 || p >= l.partitions {
		return nil, fmt.Errorf("redislog: partition %d out of range [0,%d)", p, l.partitions)
	}
	start := "-"
	if after != "" {
		start = "(" + after // exclusive of `after`
	}
	// max<=0 means unbounded; XRANGE requires a positive COUNT, so omit it.
	var msgs []redis.XMessage
	var err error
	if max > 0 {
		msgs, err = l.rdb.XRangeN(ctx, l.StreamName(p), start, "+", int64(max)).Result()
	} else {
		msgs, err = l.rdb.XRange(ctx, l.StreamName(p), start, "+").Result()
	}
	if err != nil {
		return nil, fmt.Errorf("redislog: xrange: %w", err)
	}
	return messagesToRecords(msgs)
}

// messagesToRecords decodes Redis stream messages into Records, stamping each
// record's ID with the stream message id.
func messagesToRecords(msgs []redis.XMessage) ([]Record, error) {
	out := make([]Record, 0, len(msgs))
	for _, m := range msgs {
		rec, ok, err := messageToRecord(m)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

func messageToRecord(m redis.XMessage) (Record, bool, error) {
	raw, ok := m.Values["d"].(string)
	if !ok {
		return Record{}, false, nil
	}
	var rec Record
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return Record{}, false, fmt.Errorf("redislog: unmarshal %s: %w", m.ID, err)
	}
	rec.ID = m.ID
	return rec, true, nil
}
