package window

import (
	"context"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Reaper expires time-bucketed leaderboard keys from the Redis cache once they
// are older than Retain past their window end. It sets a short TTL rather than
// deleting outright so in-flight reads complete; the durable log can always
// rebuild an expired window if it is queried again.
type Reaper struct {
	rdb    redis.UniversalClient
	retain time.Duration // grace period kept after a window ends
	ttl    time.Duration // TTL applied to aged-out keys
	now    func() time.Time
}

func NewReaper(rdb redis.UniversalClient, retain, ttl time.Duration) *Reaper {
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &Reaper{rdb: rdb, retain: retain, ttl: ttl, now: func() time.Time { return time.Now().UTC() }}
}

// windowFromZKey extracts the window id from a sorted-set key of the form
// "lb:{app:board:segment:window}:z".
func windowFromZKey(key string) (string, bool) {
	open := strings.IndexByte(key, '{')
	close := strings.IndexByte(key, '}')
	if open < 0 || close < 0 || close < open {
		return "", false
	}
	parts := strings.Split(key[open+1:close], ":")
	if len(parts) != 4 {
		return "", false
	}
	return parts[3], true
}

// Sweep scans all board sorted sets and expires those whose window has aged
// out. It returns the number of physical boards expired. Uses SCAN so it does
// not block Redis.
func (r *Reaper) Sweep(ctx context.Context) (int, error) {
	now := r.now()
	var cursor uint64
	expired := 0
	for {
		keys, next, err := r.rdb.Scan(ctx, cursor, "lb:{*}:z", 200).Result()
		if err != nil {
			return expired, err
		}
		for _, key := range keys {
			win, ok := windowFromZKey(key)
			if !ok {
				continue
			}
			end, dated := ParseEnd(win)
			if !dated {
				continue // all-time / seasonal: never auto-expire
			}
			if now.After(end.Add(r.retain)) {
				base := strings.TrimSuffix(key, ":z")
				pipe := r.rdb.Pipeline()
				pipe.Expire(ctx, key, r.ttl)
				pipe.Expire(ctx, base+":h", r.ttl)
				pipe.Expire(ctx, base+":meta", r.ttl)
				if _, err := pipe.Exec(ctx); err != nil {
					return expired, err
				}
				expired++
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return expired, nil
}

// Run sweeps on an interval until ctx is cancelled.
func (r *Reaper) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Hour
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if _, err := r.Sweep(ctx); err != nil {
				return err
			}
		}
	}
}
