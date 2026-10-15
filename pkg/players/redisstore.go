package players

import (
	"context"
	"crypto/subtle"
	"errors"

	"github.com/redis/go-redis/v9"
)

// RedisStore keeps bindings at ply:tok:{app}:{member} (no TTL: a binding is
// the identity).
type RedisStore struct {
	rdb redis.UniversalClient
}

func NewRedisStore(rdb redis.UniversalClient) *RedisStore { return &RedisStore{rdb: rdb} }

func countKey(app string) string { return "ply:count:" + app }

func tokKey(app, member string) string { return "ply:tok:" + app + ":" + member }

func (s *RedisStore) Authorize(ctx context.Context, appID, member, token string) error {
	if err := check(token); err != nil {
		return err
	}
	h := hashToken(token)
	k := tokKey(appID, member)
	bound, err := s.rdb.SetNX(ctx, k, h, 0).Result()
	if err != nil {
		return err
	}
	if bound {
		// The binding is what matters; the counter only feeds the dashboard,
		// so a failed INCR must not fail the request.
		_ = s.rdb.Incr(ctx, countKey(appID)).Err()
		return nil
	}
	cur, err := s.rdb.Get(ctx, k).Result()
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(cur), []byte(h)) != 1 {
		return ErrTokenMismatch
	}
	return nil
}

// Bindings returns the number of bindings made since the counter existed.
func (s *RedisStore) Bindings(ctx context.Context, appID string) (int64, error) {
	n, err := s.rdb.Get(ctx, countKey(appID)).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return n, err
}
