// Command loadtest drives load against the OpenLeaderboard engine (directly via
// Redis) or a running server (over HTTP), and reports throughput and latency
// percentiles. Its primary purpose is to validate the core bet — that rank-read
// latency stays flat as a board grows — and to find the point where a single
// sorted set stops scaling.
//
// Engine mode (isolates the ranking primitive):
//
//	loadtest -mode engine -redis localhost:6379 -sizes 1000,100000,1000000 \
//	         -readers 8 -writers 8 -dur 5s
//
// HTTP mode (full stack: API + ingest + consumer):
//
//	loadtest -mode http -url http://localhost:8080 -admin-token dev-admin-token \
//	         -size 100000 -readers 16 -writers 16 -dur 5s
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/araasr/leaderboard/pkg/engine"
	"github.com/araasr/leaderboard/pkg/sdk"
	"github.com/redis/go-redis/v9"
)

func main() {
	mode := flag.String("mode", "engine", "engine | http")
	redisAddr := flag.String("redis", "localhost:6379", "Redis address (engine mode)")
	url := flag.String("url", "http://localhost:8080", "server URL (http mode)")
	adminToken := flag.String("admin-token", "dev-admin-token", "admin token (http mode)")
	sizesStr := flag.String("sizes", "1000,10000,100000,1000000", "engine mode: board sizes to test read latency at")
	size := flag.Int("size", 100000, "http mode: board size to seed")
	readers := flag.Int("readers", 8, "concurrent reader goroutines")
	writers := flag.Int("writers", 8, "concurrent writer goroutines")
	durFlag := flag.Duration("dur", 5*time.Second, "duration of each measurement phase")
	flag.Parse()

	switch *mode {
	case "engine":
		runEngine(*redisAddr, parseSizes(*sizesStr), *readers, *writers, *durFlag)
	case "http":
		runHTTP(*url, *adminToken, *size, *readers, *writers, *durFlag)
	default:
		log.Fatalf("unknown mode %q (want engine|http)", *mode)
	}
}

func parseSizes(s string) []int {
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			log.Fatalf("bad size %q: %v", part, err)
		}
		out = append(out, n)
	}
	return out
}

// ---------- latency recording ----------

// recorder collects latency samples with reservoir sampling to bound memory.
type recorder struct {
	samples []time.Duration
	n       int64
	capN    int
	rng     *rand.Rand
}

func newRecorder(capN int, seed int64) *recorder {
	return &recorder{samples: make([]time.Duration, 0, capN), capN: capN, rng: rand.New(rand.NewSource(seed))}
}

func (r *recorder) record(d time.Duration) {
	r.n++
	if len(r.samples) < r.capN {
		r.samples = append(r.samples, d)
		return
	}
	// Reservoir: replace with decreasing probability.
	j := r.rng.Int63n(r.n)
	if j < int64(r.capN) {
		r.samples[j] = d
	}
}

func mergeSorted(recs []*recorder) ([]time.Duration, int64) {
	var all []time.Duration
	var total int64
	for _, r := range recs {
		all = append(all, r.samples...)
		total += r.n
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	return all, total
}

func pct(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p / 100 * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// runConcurrent runs job across `workers` goroutines until `dur` elapses,
// recording per-call latency. Returns total ops, error count, and merged
// sorted latencies.
func runConcurrent(dur time.Duration, workers int, job func(rng *rand.Rand) error) (ops int64, errs int64, sorted []time.Duration) {
	recs := make([]*recorder, workers)
	var totalOps, totalErrs int64
	deadline := time.Now().Add(dur)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		recs[w] = newRecorder(200_000, int64(w)+1)
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)*7919 + 1))
			rec := recs[w]
			for time.Now().Before(deadline) {
				start := time.Now()
				err := job(rng)
				rec.record(time.Since(start))
				atomic.AddInt64(&totalOps, 1)
				if err != nil {
					atomic.AddInt64(&totalErrs, 1)
				}
			}
		}(w)
	}
	wg.Wait()
	sorted, _ = mergeSorted(recs)
	return totalOps, totalErrs, sorted
}

func fmtThousands(n int) string {
	s := strconv.Itoa(n)
	var b strings.Builder
	pre := len(s) % 3
	for i, c := range s {
		if i != 0 && (i-pre)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func us(d time.Duration) string { return fmt.Sprintf("%.0fµs", float64(d.Microseconds())) }

// ---------- engine mode ----------

func runEngine(addr string, sizes []int, readers, writers int, dur time.Duration) {
	ctx := context.Background()
	rdb := redis.NewUniversalClient(&redis.UniversalOptions{Addrs: []string{addr}, PoolSize: readers + writers + 4})
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis unavailable at %s: %v", addr, err)
	}
	eng := engine.NewRedisEngine(rdb)

	fmt.Printf("OpenLeaderboard load test — ENGINE mode (Redis %s)\n", addr)
	fmt.Printf("NOTE: numbers reflect THIS Redis/host; not a production ElastiCache benchmark.\n\n")
	fmt.Printf("Read latency vs board size — GetRank, %d readers, %s each:\n", readers, dur)
	fmt.Printf("  %-12s %-12s %-9s %-9s %-9s %-9s\n", "size", "reads/s", "p50", "p90", "p99", "max")

	for _, size := range sizes {
		board := engine.Board{Key: engine.BoardKey{App: "bench", Board: "b", Segment: "all", Window: "all"}}
		if err := eng.Reset(ctx, board); err != nil {
			log.Fatalf("reset: %v", err)
		}
		seedBoard(ctx, eng, board, size)

		ops, errs, sorted := runConcurrent(dur, readers, func(rng *rand.Rand) error {
			m := "m" + strconv.Itoa(rng.Intn(size))
			_, err := eng.GetRank(ctx, board, m)
			if err == engine.ErrMemberNotFound {
				return nil
			}
			return err
		})
		rps := float64(ops) / dur.Seconds()
		fmt.Printf("  %-12s %-12s %-9s %-9s %-9s %-9s",
			fmtThousands(size), fmtThousands(int(rps)),
			us(pct(sorted, 50)), us(pct(sorted, 90)), us(pct(sorted, 99)), us(pct(sorted, 100)))
		if errs > 0 {
			fmt.Printf("  (errs=%d)", errs)
		}
		fmt.Println()
		// Leave the largest board seeded for the write phase.
		if size != sizes[len(sizes)-1] {
			_ = eng.Reset(ctx, board)
		}
	}

	// Write throughput against the last (largest) board.
	last := sizes[len(sizes)-1]
	board := engine.Board{Key: engine.BoardKey{App: "bench", Board: "b", Segment: "all", Window: "all"}}
	fmt.Printf("\nWrite throughput — Submit (best-wins) into a %s-member board, %d writers, %s:\n", fmtThousands(last), writers, dur)
	now := time.Now()
	ops, errs, sorted := runConcurrent(dur, writers, func(rng *rand.Rand) error {
		m := "m" + strconv.Itoa(rng.Intn(last))
		_, err := eng.Submit(ctx, board, m, float64(rng.Intn(1_000_000)), now)
		return err
	})
	wps := float64(ops) / dur.Seconds()
	fmt.Printf("  %s writes/s   p50=%s p90=%s p99=%s max=%s  (errs=%d)\n",
		fmtThousands(int(wps)), us(pct(sorted, 50)), us(pct(sorted, 90)), us(pct(sorted, 99)), us(pct(sorted, 100)), errs)

	_ = eng.Reset(ctx, board)
	fmt.Println("\nReading the table: if p99 stays roughly flat as size grows 1k->1M, rank reads")
	fmt.Println("are size-independent (the design bet). A sharp rise marks the breakpoint.")
}

// seedBoard loads `size` distinct members with random scores using pipelined
// engine submits.
func seedBoard(ctx context.Context, eng *engine.RedisEngine, board engine.Board, size int) {
	start := time.Now()
	rng := rand.New(rand.NewSource(99))
	const chunk = 5000
	ops := make([]engine.SubmitOp, 0, chunk)
	flush := func() {
		if len(ops) == 0 {
			return
		}
		if _, err := eng.SubmitBatch(ctx, ops); err != nil {
			log.Fatalf("seed: %v", err)
		}
		ops = ops[:0]
	}
	for i := 0; i < size; i++ {
		ops = append(ops, engine.SubmitOp{Board: board, Member: "m" + strconv.Itoa(i), Score: float64(rng.Intn(10_000_000))})
		if len(ops) == chunk {
			flush()
		}
	}
	flush()
	fmt.Fprintf(os.Stderr, "  seeded %s members in %s\n", fmtThousands(size), time.Since(start).Round(time.Millisecond))
}

// ---------- http mode ----------

func runHTTP(url, adminToken string, size, readers, writers int, dur time.Duration) {
	ctx := context.Background()
	// Provision an app via the admin endpoint, then a board, using a tiny client.
	key := createApp(url, adminToken)
	c := sdk.New(url, key)
	board := "bench"
	if err := c.CreateBoard(ctx, sdk.BoardDef{Board: board, UpdatePolicy: "best"}); err != nil {
		log.Fatalf("create board: %v", err)
	}

	fmt.Printf("OpenLeaderboard load test — HTTP mode (%s)\n", url)
	fmt.Printf("Seeding %s members over HTTP (write-behind)...\n", fmtThousands(size))
	seedHTTP(ctx, c, board, size)
	// Give the consumer time to apply the backlog.
	time.Sleep(2 * time.Second)

	fmt.Printf("\nRead latency — GetRank, %d readers, %s:\n", readers, dur)
	ops, errs, sorted := runConcurrent(dur, readers, func(rng *rand.Rand) error {
		_, err := c.GetRank(ctx, board, "m"+strconv.Itoa(rng.Intn(size)), sdk.QueryOpts{})
		if err == sdk.ErrNotFound {
			return nil
		}
		return err
	})
	fmt.Printf("  %s reads/s   p50=%s p90=%s p99=%s max=%s  (errs=%d)\n",
		fmtThousands(int(float64(ops)/dur.Seconds())), us(pct(sorted, 50)), us(pct(sorted, 90)), us(pct(sorted, 99)), us(pct(sorted, 100)), errs)

	fmt.Printf("\nSubmit throughput — %d writers, %s:\n", writers, dur)
	ops, errs, sorted = runConcurrent(dur, writers, func(rng *rand.Rand) error {
		_, err := c.Submit(ctx, board, sdk.Submission{Member: "m" + strconv.Itoa(rng.Intn(size)), Score: float64(rng.Intn(1_000_000))})
		return err
	})
	fmt.Printf("  %s submits/s   p50=%s p90=%s p99=%s max=%s  (errs=%d)\n",
		fmtThousands(int(float64(ops)/dur.Seconds())), us(pct(sorted, 50)), us(pct(sorted, 90)), us(pct(sorted, 99)), us(pct(sorted, 100)), errs)
}

func seedHTTP(ctx context.Context, c *sdk.Client, board string, size int) {
	var wg sync.WaitGroup
	work := make(chan int, 1000)
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				_, _ = c.Submit(ctx, board, sdk.Submission{Member: "m" + strconv.Itoa(i), Score: float64(i)})
			}
		}()
	}
	for i := 0; i < size; i++ {
		work <- i
	}
	close(work)
	wg.Wait()
}

// createApp calls the admin endpoint directly (it is not part of the tenant
// SDK) and returns the new API key.
func createApp(url, adminToken string) string {
	body := bytes.NewReader([]byte(`{"name":"loadtest"}`))
	req, err := http.NewRequest(http.MethodPost, url+"/v1/apps", body)
	if err != nil {
		log.Fatalf("create app: %v", err)
	}
	req.Header.Set("X-Admin-Token", adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("create app: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		log.Fatalf("create app: HTTP %d", resp.StatusCode)
	}
	var out struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		log.Fatalf("create app decode: %v", err)
	}
	return out.APIKey
}
