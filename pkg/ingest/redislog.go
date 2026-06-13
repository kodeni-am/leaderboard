package ingest

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// RedisLog is a durable Log backed by a Redis Stream (XADD/XRANGE). Suitable
// for single-node and self-hosted deployments. On AWS the same Log interface is
// satisfied by KinesisLog (see kinesis.go) without changing the consumer.
type RedisLog struct {
	rdb    redis.UniversalClient
	stream string
	maxLen int64 // optional approximate cap (0 = unbounded)
}

// NewRedisLog creates a stream-backed log. maxLen>0 trims the stream to roughly
// that many entries (XADD MAXLEN ~). Use 0 to retain everything (needed if the
// stream is the sole source of truth for rebuilds).
func NewRedisLog(rdb redis.UniversalClient, stream string, maxLen int64) *RedisLog {
	if stream == "" {
		stream = "lb:ingest"
	}
	return &RedisLog{rdb: rdb, stream: stream, maxLen: maxLen}
}

func (l *RedisLog) Append(ctx context.Context, rec *Record) error {
	payload, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("redislog: marshal: %w", err)
	}
	args := &redis.XAddArgs{
		Stream: l.stream,
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

func (l *RedisLog) Read(ctx context.Context, after string, max int) ([]Record, error) {
	start := "-"
	if after != "" {
		start = "(" + after // exclusive of `after`
	}
	// max<=0 means unbounded; XRANGE requires a positive COUNT, so omit it.
	var msgs []redis.XMessage
	var err error
	if max > 0 {
		msgs, err = l.rdb.XRangeN(ctx, l.stream, start, "+", int64(max)).Result()
	} else {
		msgs, err = l.rdb.XRange(ctx, l.stream, start, "+").Result()
	}
	if err != nil {
		return nil, fmt.Errorf("redislog: xrange: %w", err)
	}
	out := make([]Record, 0, len(msgs))
	for _, m := range msgs {
		raw, ok := m.Values["d"].(string)
		if !ok {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			return nil, fmt.Errorf("redislog: unmarshal %s: %w", m.ID, err)
		}
		rec.ID = m.ID
		out = append(out, rec)
	}
	return out, nil
}
