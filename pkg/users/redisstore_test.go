package users

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRedisStore(t *testing.T) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewUniversalClient(&redis.UniversalOptions{Addrs: []string{addr}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("redis not available: %v", err)
	}
	testStore(t, NewRedisStore(rdb), fmt.Sprintf("app_t%d", time.Now().UnixNano()))
}
