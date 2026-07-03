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
	"strings"
	"syscall"
	"time"

	"github.com/kodeni-am/leaderboard/pkg/accounts"
	"github.com/kodeni-am/leaderboard/pkg/api"
	"github.com/kodeni-am/leaderboard/pkg/email"
	"github.com/kodeni-am/leaderboard/pkg/engine"
	"github.com/kodeni-am/leaderboard/pkg/ingest"
	"github.com/kodeni-am/leaderboard/pkg/tenancy"
	"github.com/kodeni-am/leaderboard/pkg/users"
	"github.com/kodeni-am/leaderboard/pkg/window"
	"github.com/redis/go-redis/v9"
)

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// splitAddrs parses a single host:port or a comma-separated list of them,
// trimming whitespace and dropping blanks. More than one address makes
// go-redis use its cluster client.
func splitAddrs(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func main() {
	var (
		redisAddr   = getenv("REDIS_ADDR", "localhost:6379")
		listenAddr  = getenv("LISTEN_ADDR", ":8080")
		logBackend  = getenv("LB_LOG_BACKEND", "redis") // redis | mem
		stream      = getenv("LB_STREAM", "lb:ingest")
		signingKey  = os.Getenv("SIGNING_SECRET")
		partitions  = intEnv("INGEST_PARTITIONS", 16)
		workerIdx   = intEnv("WORKER_INDEX", 0)
		workerCnt   = intEnv("WORKER_COUNT", 1)
		boardShards = intEnv("BOARD_SHARDS", 1)
		publicURL   = getenv("PUBLIC_URL", "http://localhost:8080")
		secureCk    = os.Getenv("SECURE_COOKIES") == "true"
		corsOrigins = getenv("CORS_ORIGINS", "*")
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// REDIS_ADDR may be a single host:port or a comma-separated list of cluster
	// seed nodes. With more than one address, NewUniversalClient returns a
	// ClusterClient; with one, a plain client. The engine's per-board hash tags
	// and the per-stream ingest reads keep every command on a single slot, so
	// both paths work without code changes.
	redisAddrs := splitAddrs(redisAddr)
	rdb := redis.NewUniversalClient(&redis.UniversalOptions{Addrs: redisAddrs})
	if err := waitForRedis(ctx, rdb); err != nil {
		log.Fatalf("redis unavailable at %s: %v", redisAddr, err)
	}

	// BOARD_SHARDS > 1 splits each board across that many sorted sets (intra-board
	// sharding) so one board can exceed a single node. The default (1) uses the
	// plain single-set engine with unchanged key names. Both satisfy
	// engine.RankingEngine, so the consumer, API, and reaper are unaffected.
	var eng engine.RankingEngine
	if boardShards > 1 {
		eng = engine.NewShardedEngine(rdb, boardShards)
		log.Printf("intra-board sharding: %d shards/board (rank reads are approximate)", boardShards)
	} else {
		eng = engine.NewRedisEngine(rdb)
	}
	store := tenancy.NewRedisStore(rdb)
	registry := ingest.NewStaticRegistry()

	// Build the log. For Redis we keep the concrete type so we can run the
	// durable, partitioned consumer-group worker; mem uses the simple consumer.
	var lg ingest.Log
	var redisLog *ingest.RedisLog
	switch logBackend {
	case "mem":
		lg = ingest.NewMemLog()
	default:
		redisLog = ingest.NewRedisLog(rdb, stream, partitions, 0)
		lg = redisLog
	}
	ing := ingest.NewIngestor(lg, registry, ingest.NewRedisDeduper(rdb))

	rs := accounts.NewRedisStores(rdb)
	acctSvc := accounts.NewService(rs, rs, rs, buildMailer(), accounts.Config{BaseURL: publicURL})
	usrStore := users.NewRedisStore(rdb)

	srv := api.NewServer(eng, ing, store, registry, acctSvc, secureCk, usrStore)
	if signingKey != "" {
		srv.SetSigningMaster(signingKey, 5*time.Minute)
		log.Print("per-app HMAC signing: AVAILABLE (apps opt in via require_signing)")
	}
	if webDir := os.Getenv("WEB_DIR"); webDir != "" {
		srv.SetStaticDir(webDir)
		log.Printf("serving dashboard SPA from %s", webDir)
	}
	srv.SetCORS(corsOrigins)
	log.Printf("CORS allowed origins: %s", corsOrigins)
	if err := srv.WarmRegistry(ctx); err != nil {
		log.Fatalf("warm registry: %v", err)
	}

	// Background workers: a consumer applies the log to the engine; the reaper
	// expires aged-out time windows.
	if redisLog != nil {
		gc := ingest.NewGroupConsumer(redisLog, registry, eng, ingest.GroupOptions{
			Consumer:    getenv("HOSTNAME", "c-0"),
			WorkerIndex: workerIdx,
			WorkerCount: workerCnt,
			OnConsumed:  api.RecordConsumerApplied,
		})
		if err := gc.EnsureGroups(ctx); err != nil {
			log.Fatalf("ensure consumer groups: %v", err)
		}
		go func() {
			if err := gc.Run(ctx, 30*time.Second); err != nil && ctx.Err() == nil {
				log.Printf("group consumer stopped: %v", err)
			}
		}()
		log.Printf("group consumer: partitions=%d worker=%d/%d owns=%v", partitions, workerIdx, workerCnt, gc.Owned())
	} else {
		consumer := ingest.NewConsumer(lg, registry, eng)
		go func() {
			if err := consumer.Run(ctx, 50*time.Millisecond); err != nil && ctx.Err() == nil {
				log.Printf("consumer stopped: %v", err)
			}
		}()
	}
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

func buildMailer() email.Sender {
	host := os.Getenv("SMTP_HOST")
	if host == "" {
		log.Print("email: using console sender (set SMTP_HOST to send real mail)")
		return email.NewConsoleSender(os.Stdout)
	}
	from := getenv("SMTP_FROM", "no-reply@openleaderboard.local")
	return email.NewSMTPSender(host, intEnv("SMTP_PORT", 587), os.Getenv("SMTP_USER"), os.Getenv("SMTP_PASS"), from)
}

func intEnv(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
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
