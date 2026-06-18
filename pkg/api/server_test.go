package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kodeni-am/leaderboard/pkg/engine"
	"github.com/kodeni-am/leaderboard/pkg/ingest"
	"github.com/kodeni-am/leaderboard/pkg/tenancy"
	"github.com/redis/go-redis/v9"
)

type harness struct {
	ts   *httptest.Server
	cons *ingest.Consumer
	eng  *engine.RedisEngine
	key  string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
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
	eng := engine.NewRedisEngine(rdb)
	store := tenancy.NewMemStore()
	registry := ingest.NewStaticRegistry()
	log := ingest.NewMemLog()
	ing := ingest.NewIngestor(log, registry, ingest.NewMemDeduper())
	cons := ingest.NewConsumer(log, registry, eng)
	srv := NewServer(eng, ing, store, registry, "admin-secret")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &harness{ts: ts, cons: cons, eng: eng}
}

func (h *harness) do(t *testing.T, method, path, key string, body any) (*http.Response, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.ts.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

func TestAPIFullFlow(t *testing.T) {
	h := newHarness(t)

	// 1. Create app with the admin token.
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/v1/apps", bytes.NewReader([]byte(`{"name":"Pong"}`)))
	req.Header.Set("X-Admin-Token", "admin-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var appResp createAppResp
	json.NewDecoder(resp.Body).Decode(&appResp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || appResp.APIKey == "" {
		t.Fatalf("create app: %d key=%q", resp.StatusCode, appResp.APIKey)
	}
	key := appResp.APIKey

	// Wrong admin token is rejected.
	bad, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/v1/apps", bytes.NewReader([]byte(`{"name":"x"}`)))
	bad.Header.Set("X-Admin-Token", "nope")
	br, _ := http.DefaultClient.Do(bad)
	if br.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad admin token: got %d, want 401", br.StatusCode)
	}
	br.Body.Close()

	// 2. Create a board (desc/best defaults).
	resp, data := h.do(t, http.MethodPost, "/v1/boards", key, map[string]any{"board": "high"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create board: %d %s", resp.StatusCode, data)
	}

	// Unauthenticated request is rejected.
	resp, _ = h.do(t, http.MethodGet, "/v1/boards/high/top", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no auth: got %d, want 401", resp.StatusCode)
	}

	// 3. Submit scores (write-behind -> 202).
	for _, s := range []struct {
		m string
		v float64
	}{{"alice", 300}, {"bob", 500}, {"carol", 100}} {
		resp, data := h.do(t, http.MethodPost, "/v1/boards/high/scores", key, map[string]any{"member": s.m, "score": s.v})
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("submit %s: %d %s", s.m, resp.StatusCode, data)
		}
	}

	// Rank not visible until the consumer applies the log.
	resp, _ = h.do(t, http.MethodGet, "/v1/boards/high/rank?member=bob", key, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("rank before consume: got %d, want 404", resp.StatusCode)
	}
	if err := h.cons.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = h.eng.Reset(context.Background(), engine.Board{Key: engine.BoardKey{App: appResp.ID, Board: "high", Segment: "all", Window: "all"}})
	})

	// 4. Query rank.
	resp, data = h.do(t, http.MethodGet, "/v1/boards/high/rank?member=bob", key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rank: %d %s", resp.StatusCode, data)
	}
	var entry engine.RankEntry
	json.Unmarshal(data, &entry)
	if entry.Rank != 1 || entry.Score != 500 {
		t.Errorf("bob rank=%d score=%v, want 1/500", entry.Rank, entry.Score)
	}

	// 5. Top-N.
	resp, data = h.do(t, http.MethodGet, "/v1/boards/high/top?n=2", key, nil)
	var top struct {
		Entries []engine.RankEntry `json:"entries"`
	}
	json.Unmarshal(data, &top)
	if len(top.Entries) != 2 || top.Entries[0].Member != "bob" || top.Entries[1].Member != "alice" {
		t.Errorf("top = %+v", top.Entries)
	}

	// 6. Neighbors of alice (rank 2): bob, alice, carol.
	resp, data = h.do(t, http.MethodGet, "/v1/boards/high/neighbors?member=alice&k=1", key, nil)
	var nb struct {
		Entries []engine.RankEntry `json:"entries"`
	}
	json.Unmarshal(data, &nb)
	if len(nb.Entries) != 3 || nb.Entries[1].Member != "alice" {
		t.Errorf("neighbors = %+v", nb.Entries)
	}

	// 7. Friend rank among carol+bob.
	resp, data = h.do(t, http.MethodPost, "/v1/boards/high/friends", key, map[string]any{"members": []string{"carol", "bob"}})
	var fr struct {
		Entries []engine.RankEntry `json:"entries"`
	}
	json.Unmarshal(data, &fr)
	if len(fr.Entries) != 2 || fr.Entries[0].Member != "bob" || fr.Entries[0].Rank != 1 {
		t.Errorf("friends = %+v", fr.Entries)
	}

	// 8. Submit to an unknown board -> 404.
	resp, _ = h.do(t, http.MethodPost, "/v1/boards/ghost/scores", key, map[string]any{"member": "x", "score": 1})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown board: got %d, want 404", resp.StatusCode)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	h := newHarness(t)

	// Generate some traffic: create an app + board + a submit.
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/v1/apps", bytes.NewReader([]byte(`{"name":"Metrics"}`)))
	req.Header.Set("X-Admin-Token", "admin-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var appResp createAppResp
	json.NewDecoder(resp.Body).Decode(&appResp)
	resp.Body.Close()
	key := appResp.APIKey
	h.do(t, http.MethodPost, "/v1/boards", key, map[string]any{"board": "m"})
	h.do(t, http.MethodPost, "/v1/boards/m/scores", key, map[string]any{"member": "p", "score": 1})
	t.Cleanup(func() {
		_ = h.eng.Reset(context.Background(), engine.Board{Key: engine.BoardKey{App: appResp.ID, Board: "m", Segment: "all", Window: "all"}})
	})

	// Scrape /metrics (no auth) and check our metric families are present.
	resp, body := h.do(t, http.MethodGet, "/metrics", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics status %d", resp.StatusCode)
	}
	text := string(body)
	for _, want := range []string{
		"lb_http_requests_total",
		"lb_http_request_duration_seconds",
		"lb_submits_total",
		`route="/v1/boards/{board}/scores"`,
		`lb_submits_total{result="accepted"}`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("/metrics missing %q", want)
		}
	}
}
