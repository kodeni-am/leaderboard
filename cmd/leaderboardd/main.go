// Command leaderboardd is the OpenLeaderboard server: it wires the SP1 engine,
// SP2 ingestion log + consumer, SP3 API, SP4 window reaper, SP5 tenancy, and
// optional SP7 HMAC verification into a single process configured by env vars.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/araasr/leaderboard/pkg/api"
	"github.com/araasr/leaderboard/pkg/engine"
	"github.com/araasr/leaderboard/pkg/ingest"
	"github.com/araasr/leaderboard/pkg/tenancy"
	"github.com/araasr/leaderboard/pkg/trust"
	"github.com/araasr/leaderboard/pkg/window"
	"github.com/redis/go-redis/v9"
)

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	var (
		redisAddr  = getenv("REDIS_ADDR", "localhost:6379")
		listenAddr = getenv("LISTEN_ADDR", ":8080")
		logBackend = getenv("LB_LOG_BACKEND", "redis") // redis | mem
		stream     = getenv("LB_STREAM", "lb:ingest")
		adminToken = os.Getenv("ADMIN_TOKEN")
		signingKey = os.Getenv("SIGNING_SECRET")
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	rdb := redis.NewUniversalClient(&redis.UniversalOptions{Addrs: []string{redisAddr}})
	if err := waitForRedis(ctx, rdb); err != nil {
		log.Fatalf("redis unavailable at %s: %v", redisAddr, err)
	}

	eng := engine.NewRedisEngine(rdb)
	store := tenancy.NewRedisStore(rdb)
	registry := ingest.NewStaticRegistry()

	var lg ingest.Log
	switch logBackend {
	case "mem":
		lg = ingest.NewMemLog()
	default:
		lg = ingest.NewRedisLog(rdb, stream, 0)
	}
	ing := ingest.NewIngestor(lg, registry, ingest.NewRedisDeduper(rdb))
	consumer := ingest.NewConsumer(lg, registry, eng)

	srv := api.NewServer(eng, ing, store, registry, adminToken)
	if signingKey != "" {
		srv.SetVerifier(trust.NewVerifier(signingKey, 5*time.Minute))
		log.Print("HMAC submission verification: ENABLED")
	}
	if err := srv.WarmRegistry(ctx); err != nil {
		log.Fatalf("warm registry: %v", err)
	}

	// Background workers: the consumer applies the log to the engine; the
	// reaper expires aged-out time windows.
	go func() {
		if err := consumer.Run(ctx, 50*time.Millisecond); err != nil && ctx.Err() == nil {
			log.Printf("consumer stopped: %v", err)
		}
	}()
	if retain := os.Getenv("LB_REAPER_RETAIN"); retain != "" {
		if d, err := time.ParseDuration(retain); err == nil {
			interval := durationEnv("LB_REAPER_INTERVAL", time.Hour)
			reaper := window.NewReaper(rdb, d, time.Hour)
			go func() {
				if err := reaper.Run(ctx, interval); err != nil && ctx.Err() == nil {
					log.Printf("reaper stopped: %v", err)
				}
			}()
			log.Printf("window reaper: retain=%s interval=%s", d, interval)
		}
	}

	httpSrv := &http.Server{
		Addr:              listenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("leaderboardd listening on %s (log backend: %s)", listenAddr, logBackend)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("shutting down...")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutCtx)
}

func waitForRedis(ctx context.Context, rdb redis.UniversalClient) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := rdb.Ping(c).Err()
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return err
		}
		time.Sleep(time.Second)
	}
}

func durationEnv(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return def
}
