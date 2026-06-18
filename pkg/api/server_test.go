package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kodeni-am/leaderboard/pkg/accounts"
	"github.com/kodeni-am/leaderboard/pkg/email"
	"github.com/kodeni-am/leaderboard/pkg/engine"
	"github.com/kodeni-am/leaderboard/pkg/ingest"
	"github.com/kodeni-am/leaderboard/pkg/tenancy"
	"github.com/redis/go-redis/v9"
)

type captureSender struct {
	mu  sync.Mutex
	msg email.Message
}

func (c *captureSender) Send(_ context.Context, m email.Message) error {
	c.mu.Lock()
	c.msg = m
	c.mu.Unlock()
	return nil
}

func (c *captureSender) lastText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.msg.Text
}

func tokenFrom(text, marker string) string {
	i := strings.Index(text, marker)
	if i < 0 {
		return ""
	}
	rest := text[i+len(marker):]
	if j := strings.IndexAny(rest, "\r\n "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

type harness struct {
	ts     *httptest.Server
	cons   *ingest.Consumer
	eng    *engine.RedisEngine
	mail   *captureSender
	client *http.Client
	csrf   string
	appID  string
	apiKey string
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
	logp := ingest.NewMemLog()
	ing := ingest.NewIngestor(logp, registry, ingest.NewMemDeduper())
	cons := ingest.NewConsumer(logp, registry, eng)
	mail := &captureSender{}
	mem := accounts.NewMemStores()
	acct := accounts.NewService(mem, mem, mem, mail, accounts.Config{BaseURL: "http://app"})
	srv := NewServer(eng, ing, store, registry, acct, false)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &harness{ts: ts, cons: cons, eng: eng, mail: mail, client: client}
}

// call issues a request via the cookie-jar client.
func (h *harness) call(t *testing.T, method, path string, headers map[string]string, body any) (*http.Response, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, h.ts.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

func (h *harness) key() map[string]string {
	return map[string]string{"Authorization": "Bearer " + h.apiKey}
}
func (h *harness) sess() map[string]string {
	return map[string]string{"X-App-Id": h.appID, "X-CSRF-Token": h.csrf}
}

// onboard runs signup -> verify -> login -> create app, populating csrf/appID/apiKey.
func (h *harness) onboard(t *testing.T, emailAddr string) {
	t.Helper()
	if resp, body := h.call(t, http.MethodPost, "/auth/signup", nil, map[string]string{"email": emailAddr, "password": "hunter2hunter"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("signup: %d %s", resp.StatusCode, body)
	}
	tok := tokenFrom(h.mail.lastText(), "/auth/verify?token=")
	if tok == "" {
		t.Fatal("no verification token emailed")
	}
	if resp, _ := h.call(t, http.MethodGet, "/auth/verify?token="+tok, nil, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("verify: %d", resp.StatusCode)
	}
	resp, body := h.call(t, http.MethodPost, "/auth/login", nil, map[string]string{"email": emailAddr, "password": "hunter2hunter"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %s", resp.StatusCode, body)
	}
	var lr struct {
		CSRF string `json:"csrf_token"`
	}
	json.Unmarshal(body, &lr)
	h.csrf = lr.CSRF
	resp, body = h.call(t, http.MethodPost, "/v1/apps", map[string]string{"X-CSRF-Token": h.csrf}, map[string]string{"name": "My Game"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create app: %d %s", resp.StatusCode, body)
	}
	var ar struct {
		ID     string `json:"id"`
		APIKey string `json:"api_key"`
	}
	json.Unmarshal(body, &ar)
	h.appID, h.apiKey = ar.ID, ar.APIKey
	if h.appID == "" || h.apiKey == "" {
		t.Fatalf("app create returned empty id/key: %s", body)
	}
}

func TestAPIFullFlow(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "dev@example.com")
	t.Cleanup(func() {
		_ = h.eng.Reset(context.Background(), engine.Board{Key: engine.BoardKey{App: h.appID, Board: "high", Segment: "all", Window: "all"}})
	})

	// Apps list reflects the created app (session-authed).
	if resp, body := h.call(t, http.MethodGet, "/v1/apps", nil, nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), h.appID) {
		t.Fatalf("list apps: %d %s", resp.StatusCode, body)
	}

	// Data plane via API key: create board + submit.
	if resp, body := h.call(t, http.MethodPost, "/v1/boards", h.key(), map[string]any{"board": "high"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create board: %d %s", resp.StatusCode, body)
	}
	for _, s := range []struct {
		m string
		v float64
	}{{"alice", 300}, {"bob", 500}, {"carol", 100}} {
		if resp, body := h.call(t, http.MethodPost, "/v1/boards/high/scores", h.key(), map[string]any{"member": s.m, "score": s.v}); resp.StatusCode != http.StatusAccepted {
			t.Fatalf("submit %s: %d %s", s.m, resp.StatusCode, body)
		}
	}
	if err := h.cons.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Read via API key.
	resp, body := h.call(t, http.MethodGet, "/v1/boards/high/rank?member=bob", h.key(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rank: %d %s", resp.StatusCode, body)
	}
	var entry engine.RankEntry
	json.Unmarshal(body, &entry)
	if entry.Rank != 1 || entry.Score != 500 {
		t.Errorf("bob: %+v", entry)
	}

	// Read via SESSION + X-App-Id (no API key) — the dashboard path.
	resp, body = h.call(t, http.MethodGet, "/v1/boards/high/top?n=2", map[string]string{"X-App-Id": h.appID}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session top: %d %s", resp.StatusCode, body)
	}
	var top struct {
		Entries []engine.RankEntry `json:"entries"`
	}
	json.Unmarshal(body, &top)
	if len(top.Entries) != 2 || top.Entries[0].Member != "bob" {
		t.Errorf("session top = %+v", top.Entries)
	}

	// Unauthenticated data-plane request (fresh client, no cookies/key) -> 401.
	if r, err := http.Get(h.ts.URL + "/v1/boards/high/top?n=2"); err != nil {
		t.Fatal(err)
	} else {
		r.Body.Close()
		if r.StatusCode != http.StatusUnauthorized {
			t.Errorf("unauth data-plane: got %d, want 401", r.StatusCode)
		}
	}

	// Session mutation without CSRF -> 403.
	if resp, _ := h.call(t, http.MethodPost, "/v1/apps", nil, map[string]string{"name": "no-csrf"}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("missing csrf: got %d, want 403", resp.StatusCode)
	}
}

func TestAuthFlows(t *testing.T) {
	h := newHarness(t)

	// Duplicate signup -> 409.
	h.call(t, http.MethodPost, "/auth/signup", nil, map[string]string{"email": "dup@x.co", "password": "hunter2hunter"})
	if resp, _ := h.call(t, http.MethodPost, "/auth/signup", nil, map[string]string{"email": "dup@x.co", "password": "hunter2hunter"}); resp.StatusCode != http.StatusConflict {
		t.Errorf("dup signup: got %d, want 409", resp.StatusCode)
	}

	// Login before verification -> 403.
	if resp, _ := h.call(t, http.MethodPost, "/auth/login", nil, map[string]string{"email": "dup@x.co", "password": "hunter2hunter"}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("login unverified: got %d, want 403", resp.StatusCode)
	}

	// Onboard a different user, then log out and confirm /auth/me is rejected.
	h.onboard(t, "flows@example.com")
	if resp, _ := h.call(t, http.MethodGet, "/auth/me", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("me before logout: %d", resp.StatusCode)
	}
	if resp, _ := h.call(t, http.MethodPost, "/auth/logout", map[string]string{"X-CSRF-Token": h.csrf}, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if resp, _ := h.call(t, http.MethodGet, "/auth/me", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("me after logout: got %d, want 401", resp.StatusCode)
	}
}

func TestKeyManagement(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "keys@example.com")
	t.Cleanup(func() {
		h.call(t, http.MethodDelete, "/v1/apps/"+h.appID, map[string]string{"X-CSRF-Token": h.csrf}, nil)
	})

	withKey := func(k string) map[string]string { return map[string]string{"Authorization": "Bearer " + k} }

	// Exactly one key after app creation; it has a masked prefix.
	_, body := h.call(t, http.MethodGet, "/v1/apps/"+h.appID+"/keys", nil, nil)
	var lk struct {
		Keys []struct {
			ID     string `json:"id"`
			Prefix string `json:"prefix"`
		} `json:"keys"`
	}
	json.Unmarshal(body, &lk)
	if len(lk.Keys) != 1 || lk.Keys[0].Prefix == "" {
		t.Fatalf("initial keys: %s", body)
	}
	origKeyID := lk.Keys[0].ID

	// Issue a second key — zero-downtime rotation: both keys work.
	resp, body := h.call(t, http.MethodPost, "/v1/apps/"+h.appID+"/keys", map[string]string{"X-CSRF-Token": h.csrf}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("issue key: %d %s", resp.StatusCode, body)
	}
	var nk struct {
		APIKey string `json:"api_key"`
	}
	json.Unmarshal(body, &nk)
	if r, _ := h.call(t, http.MethodGet, "/v1/boards", withKey(h.apiKey), nil); r.StatusCode != http.StatusOK {
		t.Errorf("original key should still work: %d", r.StatusCode)
	}
	if r, _ := h.call(t, http.MethodGet, "/v1/boards", withKey(nk.APIKey), nil); r.StatusCode != http.StatusOK {
		t.Errorf("new key should work: %d", r.StatusCode)
	}

	// Revoke the original key: it stops working, the new one still does.
	if r, _ := h.call(t, http.MethodDelete, "/v1/apps/"+h.appID+"/keys/"+origKeyID, map[string]string{"X-CSRF-Token": h.csrf}, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("revoke key: %d", r.StatusCode)
	}
	if r, _ := h.call(t, http.MethodGet, "/v1/boards", withKey(h.apiKey), nil); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("revoked key should be 401, got %d", r.StatusCode)
	}
	if r, _ := h.call(t, http.MethodGet, "/v1/boards", withKey(nk.APIKey), nil); r.StatusCode != http.StatusOK {
		t.Errorf("surviving key should still work, got %d", r.StatusCode)
	}

	// Delete the app: remaining key stops working and it leaves the owner's list.
	if r, _ := h.call(t, http.MethodDelete, "/v1/apps/"+h.appID, map[string]string{"X-CSRF-Token": h.csrf}, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("delete app: %d", r.StatusCode)
	}
	if r, _ := h.call(t, http.MethodGet, "/v1/boards", withKey(nk.APIKey), nil); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("key after app delete should be 401, got %d", r.StatusCode)
	}
	_, body = h.call(t, http.MethodGet, "/v1/apps", nil, nil)
	if s := string(body); !strings.Contains(s, `"apps":[]`) && !strings.Contains(s, `"apps":null`) {
		t.Errorf("owner should have no apps after delete: %s", body)
	}
}

func TestApproxRankEndpoint(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "approx@example.com")
	t.Cleanup(func() {
		_ = h.eng.Reset(context.Background(), engine.Board{Key: engine.BoardKey{App: h.appID, Board: "big", Segment: "all", Window: "all"}})
		_ = h.eng.Reset(context.Background(), engine.Board{Key: engine.BoardKey{App: h.appID, Board: "plain", Segment: "all", Window: "all"}})
	})

	// Create an approx-enabled board.
	if resp, body := h.call(t, http.MethodPost, "/v1/boards", h.key(), map[string]any{
		"board": "big", "approx_rank": true, "approx_min": 0, "approx_max": 1000, "approx_buckets": 1000,
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create approx board: %d %s", resp.StatusCode, body)
	}
	for _, s := range []struct {
		m string
		v float64
	}{{"alice", 300}, {"bob", 500}, {"carol", 100}} {
		if resp, body := h.call(t, http.MethodPost, "/v1/boards/big/scores", h.key(), map[string]any{"member": s.m, "score": s.v}); resp.StatusCode != http.StatusAccepted {
			t.Fatalf("submit %s: %d %s", s.m, resp.StatusCode, body)
		}
	}
	if err := h.cons.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Approximate read: bob (500) is rank 1, flagged inexact.
	resp, body := h.call(t, http.MethodGet, "/v1/boards/big/rank?member=bob&approx=true", h.key(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approx rank: %d %s", resp.StatusCode, body)
	}
	var entry engine.RankEntry
	json.Unmarshal(body, &entry)
	if entry.Rank != 1 || entry.Exact {
		t.Errorf("approx bob: %+v (want rank 1, exact=false)", entry)
	}

	// approx=true on a board without the tier -> 400.
	h.call(t, http.MethodPost, "/v1/boards", h.key(), map[string]any{"board": "plain"})
	if resp, _ := h.call(t, http.MethodGet, "/v1/boards/plain/rank?member=x&approx=true", h.key(), nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("approx on plain board: got %d, want 400", resp.StatusCode)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "metrics@example.com")
	t.Cleanup(func() {
		_ = h.eng.Reset(context.Background(), engine.Board{Key: engine.BoardKey{App: h.appID, Board: "m", Segment: "all", Window: "all"}})
	})
	h.call(t, http.MethodPost, "/v1/boards", h.key(), map[string]any{"board": "m"})
	h.call(t, http.MethodPost, "/v1/boards/m/scores", h.key(), map[string]any{"member": "p", "score": 1})

	resp, body := h.call(t, http.MethodGet, "/metrics", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics status %d", resp.StatusCode)
	}
	text := string(body)
	for _, want := range []string{
		"lb_http_requests_total",
		"lb_submits_total",
		`lb_submits_total{result="accepted"}`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("/metrics missing %q", want)
		}
	}
}
