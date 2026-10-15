# Player Tokens & Key Scopes (OLB server, dashboard, SDK) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a player's leaderboard identity provable (a per-player token bound on first use) and split API keys into `secret` and `publishable`, so a key extracted from a game client can no longer rename, delete, impersonate or moderate players.

**Architecture:** A new `pkg/players` store keeps `sha256(token)` per (app, member), bound with SETNX. `pkg/api` checks it on every player-scoped write (submit, user create/rename/delete). API keys gain a scope that the `requireApp` middleware puts in the request context. Admin routes reject publishable keys. A per-app `require_player_tokens` switch (mirroring `require_signing`) turns "token optional" into "token required" for publishable keys. SDK 0.7.0 sends `X-Player-Token`.

**Tech Stack:** Go 1.x (`net/http` ServeMux patterns, go-redis `UniversalClient`), React + Vite dashboard (`web/`), TypeScript SDK (`sdk/typescript`, node test scripts).

**Spec:** `~/Projects/swell/docs/superpowers/specs/2026-10-11-player-tokens-design.md`. The companion client plan is `~/Projects/swell/docs/superpowers/plans/2026-10-11-player-tokens-client.md`, which depends on Tasks 1–6 here being deployed and SDK 0.7.0 being published.

## Global Constraints

- Header name: `X-Player-Token`. Token format accepted: 32–128 characters, `[0-9a-zA-Z_-]`.
- Redis key for bindings: `ply:tok:{app}:{member}` (value = hex sha256 of the token, no TTL).
- Error codes (JSON `{"error": …}`): `player_token_required` (401), `player_token_mismatch` (403), `invalid_player_token` (400), `secret_key_required` (403).
- Key scopes: `secret` | `publishable`. A missing scope on stored keys means `secret`. Every existing key stays secret.
- Session (dashboard) auth counts as `secret`.
- A token, when sent, is always checked, whatever the key scope.
- `NewServer`'s signature does not change (it is used in tests and `cmd/leaderboardd`). New dependencies go in through setters, like `SetSigningMaster`.
- Tests: `make test` (Docker, Redis-backed). API tests use `newHarness` in `pkg/api/server_test.go` and skip without Redis.
- SDK version: `0.7.0`. Publishing to npm is the owner's step (outward-facing). Prepare it, don't publish.

## Review Focus

1. **Bind race:** two first writes for the same new member with different tokens arrive at once. Exactly one must bind, and the other must get `player_token_mismatch`. This is pinned by a concurrent test in Task 1.
2. **Revoked publishable key:** it must stop authenticating, and its scope marker must not leak onto a re-issued hash. Pinned in Task 2.
3. **Secret key + wrong token:** the Worker forwards a bad token. It must still be rejected (403), and must not pass just because the key is secret. Pinned in Task 4.
4. **User create fails after binding:** `POST /v1/users {member, nickname}` where the nickname is taken. The binding made before Create stays (harmless, it is the caller's member), and a retry with the same token succeeds. Pinned in Task 4.
5. **Legacy keys with no scope field:** key metadata written before this change has no `scope`. Listing shows it as `secret`, and auth treats it as secret. Pinned in Task 2.

---

### Task 1: `pkg/players` token store

**Files:**
- Create: `pkg/players/store.go`, `pkg/players/memstore.go`, `pkg/players/redisstore.go`
- Test: `pkg/players/players_test.go`

**Interfaces:**
- Produces:
  - `players.ErrTokenRequired`, `players.ErrTokenMismatch`, `players.ErrInvalidToken` (errors), `players.ValidToken(t string) bool`
  - `type Store interface { Authorize(ctx context.Context, appID, member, token string) error }`
  - `players.NewMemStore() *MemStore`, `players.NewRedisStore(rdb redis.UniversalClient) *RedisStore`

- [ ] **Step 1: Write the failing tests**

```go
// pkg/players/players_test.go
package players

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

const tokA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const tokB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func contract(t *testing.T, s Store, app string) {
	ctx := context.Background()
	if err := s.Authorize(ctx, app, "m1", ""); !errors.Is(err, ErrTokenRequired) {
		t.Fatalf("empty token: %v", err)
	}
	if err := s.Authorize(ctx, app, "m1", "short"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("short token: %v", err)
	}
	if err := s.Authorize(ctx, app, "m1", strings.Repeat("!", 40)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("bad charset: %v", err)
	}
	if err := s.Authorize(ctx, app, "m1", tokA); err != nil {
		t.Fatalf("first use binds: %v", err)
	}
	if err := s.Authorize(ctx, app, "m1", tokA); err != nil {
		t.Fatalf("same token again: %v", err)
	}
	if err := s.Authorize(ctx, app, "m1", tokB); !errors.Is(err, ErrTokenMismatch) {
		t.Fatalf("other token: %v", err)
	}
	// bindings are per member and per app
	if err := s.Authorize(ctx, app, "m2", tokB); err != nil {
		t.Fatalf("other member binds independently: %v", err)
	}
	if err := s.Authorize(ctx, app+"x", "m1", tokB); err != nil {
		t.Fatalf("other app binds independently: %v", err)
	}
	// concurrent first use: exactly one winner
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i, tok := range []string{tokA, tokB, tokA + "c", tokB + "d"} {
		wg.Add(1)
		go func(i int, tok string) {
			defer wg.Done()
			if s.Authorize(ctx, app, "race", tok) == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(i, tok)
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("race: %d winners, want 1", wins)
	}
}

func TestMemStore(t *testing.T) { contract(t, NewMemStore(), "app_mem") }

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
	app := "app_test_" + time.Now().Format("150405.000000")
	t.Cleanup(func() {
		keys, _ := rdb.Keys(context.Background(), "ply:tok:"+app+"*").Result()
		if len(keys) > 0 {
			rdb.Del(context.Background(), keys...)
		}
	})
	contract(t, NewRedisStore(rdb), app)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `docker compose run --rm app go test ./pkg/players/... -count=1`
Expected: FAIL to compile, with `undefined: Store` / `NewMemStore`.

- [ ] **Step 3: Implement**

```go
// pkg/players/store.go
// Package players proves who a leaderboard member is: a per-player token,
// bound to (app, member) on first use. Member ids are public (every board
// read returns them), so they cannot double as credentials; the token never
// leaves the player's install and the server keeps only its sha256.
package players

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

var (
	ErrTokenRequired = errors.New("players: player token required")
	ErrTokenMismatch = errors.New("players: player token mismatch")
	ErrInvalidToken  = errors.New("players: player token must be 32-128 characters of [0-9a-zA-Z_-]")
)

// Store binds and checks player tokens.
type Store interface {
	// Authorize binds token to (appID, member) if the member has no token yet
	// (atomic: concurrent first uses produce exactly one binding), otherwise
	// requires the same token. Returns ErrTokenRequired for "", ErrInvalidToken
	// for a malformed token, ErrTokenMismatch for a different one.
	Authorize(ctx context.Context, appID, member, token string) error
}

// ValidToken reports whether t is a well-formed player token.
func ValidToken(t string) bool {
	if len(t) < 32 || len(t) > 128 {
		return false
	}
	for _, r := range t {
		ok := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

func check(token string) error {
	if token == "" {
		return ErrTokenRequired
	}
	if !ValidToken(token) {
		return ErrInvalidToken
	}
	return nil
}
```

```go
// pkg/players/memstore.go
package players

import (
	"context"
	"crypto/subtle"
	"sync"
)

// MemStore is an in-memory Store for tests and single-process runs.
type MemStore struct {
	mu sync.Mutex
	m  map[string]string // app + "\x00" + member -> token hash
}

func NewMemStore() *MemStore { return &MemStore{m: map[string]string{}} }

func (s *MemStore) Authorize(_ context.Context, appID, member, token string) error {
	if err := check(token); err != nil {
		return err
	}
	h := hashToken(token)
	k := appID + "\x00" + member
	s.mu.Lock()
	defer s.mu.Unlock()
	bound, ok := s.m[k]
	if !ok {
		s.m[k] = h
		return nil
	}
	if subtle.ConstantTimeCompare([]byte(bound), []byte(h)) != 1 {
		return ErrTokenMismatch
	}
	return nil
}
```

```go
// pkg/players/redisstore.go
package players

import (
	"context"
	"crypto/subtle"

	"github.com/redis/go-redis/v9"
)

// RedisStore keeps bindings at ply:tok:{app}:{member} (no TTL: a binding is
// the identity).
type RedisStore struct {
	rdb redis.UniversalClient
}

func NewRedisStore(rdb redis.UniversalClient) *RedisStore { return &RedisStore{rdb: rdb} }

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
```

(Check the go-redis import path against `pkg/users/redisstore.go`. Use whatever that file imports.)

- [ ] **Step 4: Run to verify it passes**

Run: `docker compose run --rm app go test ./pkg/players/... -count=1 -v`
Expected: PASS (`TestMemStore`, `TestRedisStore`)

- [ ] **Step 5: Commit**

```bash
git add pkg/players
git commit -m "feat(players): per-player token store, bound on first use"
```

---

### Task 2: Key scopes in `pkg/tenancy`

**Files:**
- Modify: `pkg/tenancy/store.go` (APIKey, Store interface), `pkg/tenancy/memstore.go`, `pkg/tenancy/redisstore.go`
- Modify callers of `IssueKey`: `pkg/tenancy/*` `CreateApp`, `pkg/api/auth.go` `handleIssueKey` (just pass `tenancy.ScopeSecret` for now; Task 3 reads the body)
- Test: `pkg/tenancy/tenancy_test.go`

**Interfaces:**
- Produces:
  - `type KeyScope string`, `tenancy.ScopeSecret KeyScope = "secret"`, `tenancy.ScopePublishable KeyScope = "publishable"`, `func ParseScope(s string) (KeyScope, bool)`
  - `APIKey.Scope KeyScope` (json `scope`)
  - `Store.IssueKey(ctx, appID string, scope KeyScope) (string, APIKey, error)`
  - `Store.AuthKey(ctx, plaintextKey string) (App, KeyScope, error)`. `AppByKey` remains, implemented via `AuthKey`.

- [ ] **Step 1: Write the failing tests** (append to `pkg/tenancy/tenancy_test.go`. Use the file's existing store constructor pattern, so the Redis variant runs too if that file has one.)

```go
func TestKeyScopes(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	app, secretPlain, err := s.CreateApp(ctx, "usr_1", "scoped")
	if err != nil {
		t.Fatal(err)
	}
	if _, scope, err := s.AuthKey(ctx, secretPlain); err != nil || scope != ScopeSecret {
		t.Fatalf("app's first key is secret: %v %v", scope, err)
	}
	pubPlain, pub, err := s.IssueKey(ctx, app.ID, ScopePublishable)
	if err != nil || pub.Scope != ScopePublishable {
		t.Fatalf("issue publishable: %+v %v", pub, err)
	}
	gotApp, scope, err := s.AuthKey(ctx, pubPlain)
	if err != nil || scope != ScopePublishable || gotApp.ID != app.ID {
		t.Fatalf("auth publishable: %v %v %v", gotApp.ID, scope, err)
	}
	keys, _ := s.ListKeys(ctx, app.ID)
	seen := map[KeyScope]int{}
	for _, k := range keys {
		seen[k.Scope]++
	}
	if seen[ScopeSecret] != 1 || seen[ScopePublishable] != 1 {
		t.Fatalf("list scopes: %+v", keys)
	}
	if err := s.RevokeKey(ctx, app.ID, pub.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AuthKey(ctx, pubPlain); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("revoked publishable must not auth: %v", err)
	}
	if _, ok := ParseScope(""); !ok {
		t.Fatal(`"" parses (as secret)`)
	}
	if _, ok := ParseScope("admin"); ok {
		t.Fatal("unknown scope must not parse")
	}
}
```

Add a Redis-only test for metadata written before this change (no `scope` field), in the Redis section of that test file:

```go
// legacy meta: written before scopes existed
legacy := APIKey{ID: "key_legacy", AppID: app.ID, Prefix: "lb_old…0000"}
data, _ := json.Marshal(struct {
	ID, AppID, Prefix string
}{legacy.ID, legacy.AppID, legacy.Prefix}) // no scope
rdb.Set(ctx, keyMetaKey("key_legacy"), data, 0)
rdb.SAdd(ctx, appKeysKey(app.ID), "key_legacy")
keys, _ := s.ListKeys(ctx, app.ID)
for _, k := range keys {
	if k.ID == "key_legacy" && k.Scope != ScopeSecret {
		t.Fatalf("legacy key lists as %q, want secret", k.Scope)
	}
}
```

(Match the JSON field names the store actually writes: `APIKey` marshals as `id`/`app_id`/`prefix`, so build the legacy blob with `map[string]string{"id": "key_legacy", "app_id": app.ID, "prefix": "lb_old…0000"}`.)

- [ ] **Step 2: Run to verify it fails**

Run: `docker compose run --rm app go test ./pkg/tenancy/... -count=1`
Expected: FAIL to compile (`undefined: ScopeSecret`, `AuthKey`).

- [ ] **Step 3: Implement**

In `store.go`:

```go
// KeyScope limits what an API key may do. Secret keys (the default, and every
// key issued before scopes existed) may call the whole data plane. Publishable
// keys are meant to ship in game clients: reads plus player-scoped writes,
// which are guarded by player tokens (pkg/players), never admin routes.
type KeyScope string

const (
	ScopeSecret      KeyScope = "secret"
	ScopePublishable KeyScope = "publishable"
)

// ParseScope maps an API/dashboard value to a scope; "" means secret.
func ParseScope(s string) (KeyScope, bool) {
	switch KeyScope(s) {
	case "", ScopeSecret:
		return ScopeSecret, true
	case ScopePublishable:
		return ScopePublishable, true
	}
	return "", false
}

func normScope(s KeyScope) KeyScope {
	if s == "" {
		return ScopeSecret
	}
	return s
}
```

Add `Scope KeyScope `json:"scope"`` to `APIKey`. Change the interface:

```go
	// IssueKey mints an additional API key with the given scope …
	IssueKey(ctx context.Context, appID string, scope KeyScope) (plaintext string, key APIKey, err error)
	// AuthKey authenticates a plaintext key to its tenant and scope.
	AuthKey(ctx context.Context, plaintextKey string) (App, KeyScope, error)
```

Redis (`redisstore.go`):
- Add `func keyScopeKey(hash string) string { return "ten:keyscope:" + hash }`.
- In `IssueKey`, set `k.Scope = normScope(scope)`. In the pipeline, when `k.Scope == ScopePublishable`, add `pipe.Set(ctx, keyScopeKey(hash), string(ScopePublishable), 0)`.
- In `CreateApp`, call `s.IssueKey(ctx, id, ScopeSecret)`.
- `AuthKey`: do the same lookup as today's `AppByKey`, then `sc, err := s.rdb.Get(ctx, keyScopeKey(h)).Result()`. A `redis.Nil` result → `ScopeSecret`, otherwise `KeyScope(sc)`.
- `AppByKey(ctx, k)`: `app, _, err := s.AuthKey(ctx, k); return app, err`.
- In `RevokeKey`, wherever it deletes `apiKeyKey(hash)`, also delete `keyScopeKey(hash)` in the same pipeline.
- In `ListKeys`, after unmarshal, set `k.Scope = normScope(k.Scope)`.

Mem (`memstore.go`): add a `keyScope map[string]KeyScope // keyHash -> scope` field (initialized in `NewMemStore`). `IssueKey` stores `normScope(scope)` in it and in `k.Scope`. `AuthKey` reads it (missing → secret). `RevokeKey` deletes it. `AppByKey` wraps `AuthKey`.

`pkg/api/auth.go` `handleIssueKey`: `s.store.IssueKey(r.Context(), app.ID, tenancy.ScopeSecret)` (Task 3 replaces this).

- [ ] **Step 4: Run to verify it passes**

Run: `docker compose run --rm app go test ./pkg/tenancy/... ./pkg/api/... -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/tenancy pkg/api/auth.go
git commit -m "feat(tenancy): secret and publishable API key scopes"
```

---

### Task 3: Enforce key scope in the API

**Files:**
- Modify: `pkg/api/auth.go` (`requireApp`, ctx key, `handleIssueKey`), `pkg/api/server.go` (route table)
- Test: `pkg/api/scopes_test.go` (new)

**Interfaces:**
- Consumes: `tenancy.AuthKey`, `tenancy.ParseScope`, `tenancy.ScopeSecret/ScopePublishable`
- Produces: `func keyScopeFrom(ctx context.Context) tenancy.KeyScope`, `func (s *Server) secretOnly(next http.HandlerFunc) http.HandlerFunc`, and the `POST /v1/apps/{id}/keys` body `{"scope": "publishable"}` → response `{"id","prefix","api_key","scope"}`. `GET .../keys` items carry `scope`.

- [ ] **Step 1: Write the failing test**

```go
// pkg/api/scopes_test.go
package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// issuePublishable mints a publishable key for the harness app via the dashboard API.
func (h *harness) issuePublishable(t *testing.T) map[string]string {
	t.Helper()
	resp, body := h.call(t, http.MethodPost, "/v1/apps/"+h.appID+"/keys", h.sess(), map[string]string{"scope": "publishable"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("issue publishable: %d %s", resp.StatusCode, body)
	}
	var k struct {
		APIKey string `json:"api_key"`
		Scope  string `json:"scope"`
	}
	json.Unmarshal(body, &k)
	if k.Scope != "publishable" {
		t.Fatalf("scope echoed: %s", body)
	}
	return map[string]string{"Authorization": "Bearer " + k.APIKey}
}

func TestPublishableKeyScope(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "scopes@example.com")
	if resp, body := h.call(t, http.MethodPost, "/v1/boards", h.key(), map[string]any{"board": "hs"}); resp.StatusCode >= 300 {
		t.Fatalf("secret key creates board: %d %s", resp.StatusCode, body)
	}
	pub := h.issuePublishable(t)

	// admin routes: refused
	if resp, body := h.call(t, http.MethodPost, "/v1/boards", pub, map[string]any{"board": "hs2"}); resp.StatusCode != http.StatusForbidden || !contains(body, "secret_key_required") {
		t.Fatalf("publishable create board: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.call(t, http.MethodDelete, "/v1/boards/hs/scores/someone", pub, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("publishable remove score: %d %s", resp.StatusCode, body)
	}
	// player routes and reads: allowed (no tokens required yet)
	if resp, body := h.call(t, http.MethodPost, "/v1/boards/hs/scores", pub, map[string]any{"member": "surfer-1", "score": 10}); resp.StatusCode >= 300 {
		t.Fatalf("publishable submit: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.call(t, http.MethodGet, "/v1/boards/hs/top?n=5", pub, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("publishable read: %d %s", resp.StatusCode, body)
	}
	// unknown scope rejected
	if resp, _ := h.call(t, http.MethodPost, "/v1/apps/"+h.appID+"/keys", h.sess(), map[string]string{"scope": "admin"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown scope: %d", resp.StatusCode)
	}
	// list shows scopes
	_, body := h.call(t, http.MethodGet, "/v1/apps/"+h.appID+"/keys", h.sess(), nil)
	if !contains(body, `"scope":"publishable"`) || !contains(body, `"scope":"secret"`) {
		t.Fatalf("list scopes: %s", body)
	}
}

func contains(b []byte, s string) bool { return strings.Contains(string(b), s) }
```

(If `grep -n "func contains" pkg/api/*_test.go` finds an existing helper with the same signature, delete this one.)

- [ ] **Step 2: Run to verify it fails**

Run: `docker compose run --rm app go test ./pkg/api/ -run TestPublishableKeyScope -count=1`
Expected: FAIL (`issue publishable … scope echoed`, since the scope is missing from the response).

- [ ] **Step 3: Implement**

`pkg/api/auth.go`:

```go
const (
	userCtxKey  ctxKey = 1
	scopeCtxKey ctxKey = 2
)

// keyScopeFrom is the caller's key scope; dashboard sessions are secret.
func keyScopeFrom(ctx context.Context) tenancy.KeyScope {
	if sc, ok := ctx.Value(scopeCtxKey).(tenancy.KeyScope); ok {
		return sc
	}
	return tenancy.ScopeSecret
}
```

(Replace the existing `const userCtxKey ctxKey = 1` with the block above.)

In `requireApp`, replace the API-key branch:

```go
		if key := bearerToken(r); key != "" {
			app, scope, err := s.store.AuthKey(r.Context(), key)
			if err != nil {
				writeErr(w, http.StatusUnauthorized, "invalid api key")
				return
			}
			ctx := context.WithValue(tenancy.WithApp(r.Context(), app), scopeCtxKey, scope)
			next(w, r.WithContext(ctx))
			return
		}
```

Add:

```go
// secretOnly guards app-wide (admin) data-plane routes: a publishable key —
// the kind shipped inside game clients — must never create boards or
// moderate other players.
func (s *Server) secretOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if keyScopeFrom(r.Context()) != tenancy.ScopeSecret {
			writeErr(w, http.StatusForbidden, "secret_key_required")
			return
		}
		next(w, r)
	}
}
```

`handleIssueKey`:

```go
func (s *Server) handleIssueKey(w http.ResponseWriter, r *http.Request) {
	app, ok := s.ownedApp(w, r)
	if !ok {
		return
	}
	var req struct {
		Scope string `json:"scope"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
	}
	scope, valid := tenancy.ParseScope(req.Scope)
	if !valid {
		writeErr(w, http.StatusBadRequest, "scope must be secret or publishable")
		return
	}
	plain, key, err := s.store.IssueKey(r.Context(), app.ID, scope)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Plaintext is shown exactly once.
	writeJSON(w, http.StatusCreated, map[string]any{"id": key.ID, "prefix": key.Prefix, "api_key": plain, "scope": key.Scope})
}
```

`pkg/api/server.go` route table:

```go
	dataPlane("POST /v1/boards", s.secretOnly(s.handleCreateBoard))
	…
	dataPlane("DELETE /v1/boards/{board}/scores/{member}", s.secretOnly(s.handleRemoveScore))
```

- [ ] **Step 4: Run to verify it passes**

Run: `make test`
Expected: PASS (all packages)

- [ ] **Step 5: Commit**

```bash
git add pkg/api
git commit -m "feat(api): publishable keys cannot reach admin routes; issue keys with a scope"
```

---

### Task 4: Player tokens on player-scoped writes + `require_player_tokens`

**Files:**
- Modify: `pkg/tenancy/store.go` (App field, `SetRequirePlayerTokens`), `pkg/tenancy/memstore.go`, `pkg/tenancy/redisstore.go`
- Modify: `pkg/api/server.go` (Server field, setter, submit, routes), `pkg/api/users.go` (create/rename), `pkg/api/moderation.go` (`handleDeleteUser`), `pkg/api/auth.go` (player-token settings handlers)
- Modify: `cmd/leaderboardd/main.go` (wire the Redis store)
- Test: `pkg/api/playertokens_test.go` (new)

**Interfaces:**
- Consumes: `players.Store`, `players.Err*`, `keyScopeFrom`, `tenancy.App`
- Produces:
  - `App.RequirePlayerTokens bool` (json `require_player_tokens,omitempty`), `Store.SetRequirePlayerTokens(ctx, appID string, require bool) (App, error)`
  - `func (s *Server) SetPlayers(p players.Store)`
  - `func (s *Server) authorizePlayer(w http.ResponseWriter, r *http.Request, app tenancy.App, member string) bool`
  - Routes `GET /v1/apps/{id}/player-tokens` and `PUT /v1/apps/{id}/player-tokens` (body/response `{"require_player_tokens": bool}`)

- [ ] **Step 1: Write the failing test**

```go
// pkg/api/playertokens_test.go
package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const ptA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const ptB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func with(h map[string]string, token string) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		out[k] = v
	}
	if token != "" {
		out["X-Player-Token"] = token
	}
	return out
}

func TestPlayerTokens(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "tokens@example.com")
	h.call(t, http.MethodPost, "/v1/boards", h.key(), map[string]any{"board": "hs"})
	pub := h.issuePublishable(t)
	submit := func(hdr map[string]string, member string) (int, string) {
		resp, body := h.call(t, http.MethodPost, "/v1/boards/hs/scores", hdr, map[string]any{"member": member, "score": 10})
		return resp.StatusCode, string(body)
	}

	// first use binds; same token passes; other token is refused
	if c, b := submit(with(pub, ptA), "surfer-1"); c >= 300 {
		t.Fatalf("bind: %d %s", c, b)
	}
	if c, b := submit(with(pub, ptA), "surfer-1"); c >= 300 {
		t.Fatalf("same token: %d %s", c, b)
	}
	if c, b := submit(with(pub, ptB), "surfer-1"); c != http.StatusForbidden || !strings.Contains(b, "player_token_mismatch") {
		t.Fatalf("other token: %d %s", c, b)
	}
	// malformed token
	if c, b := submit(with(pub, "short"), "surfer-1"); c != http.StatusBadRequest || !strings.Contains(b, "invalid_player_token") {
		t.Fatalf("malformed: %d %s", c, b)
	}
	// secret key + wrong token is still refused (the Worker passes tokens through)
	if c, b := submit(with(h.key(), ptB), "surfer-1"); c != http.StatusForbidden {
		t.Fatalf("secret+wrong token: %d %s", c, b)
	}
	// no token: allowed while the switch is off
	if c, b := submit(pub, "surfer-legacy"); c >= 300 {
		t.Fatalf("no token, switch off: %d %s", c, b)
	}

	// rename / delete / claim are guarded the same way
	resp, body := h.call(t, http.MethodPost, "/v1/users", with(pub, ptA), map[string]string{"nickname": "Wave", "member": "surfer-1"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("claim own member: %d %s", resp.StatusCode, body)
	}
	resp, body = h.call(t, http.MethodPost, "/v1/users", with(pub, ptB), map[string]string{"nickname": "Thief", "member": "surfer-1"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("claim someone else's member: %d %s", resp.StatusCode, body)
	}
	if resp, body = h.call(t, http.MethodPatch, "/v1/users/surfer-1", with(pub, ptB), map[string]string{"nickname": "Stolen"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("rename with other token: %d %s", resp.StatusCode, body)
	}
	if resp, body = h.call(t, http.MethodDelete, "/v1/users/surfer-1", with(pub, ptB), nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("delete with other token: %d %s", resp.StatusCode, body)
	}
	if resp, body = h.call(t, http.MethodPatch, "/v1/users/surfer-1", with(pub, ptA), map[string]string{"nickname": "Wave2"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("rename with own token: %d %s", resp.StatusCode, body)
	}

	// failed create keeps the binding; retry with the same token succeeds
	h.call(t, http.MethodPost, "/v1/users", h.key(), map[string]string{"nickname": "Taken"})
	if resp, _ = h.call(t, http.MethodPost, "/v1/users", with(pub, ptB), map[string]string{"nickname": "Taken", "member": "surfer-2"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("taken nickname: %d", resp.StatusCode)
	}
	if resp, body = h.call(t, http.MethodPost, "/v1/users", with(pub, ptB), map[string]string{"nickname": "Free", "member": "surfer-2"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("retry after taken: %d %s", resp.StatusCode, body)
	}

	// minted plr_ ids bind the token too
	resp, body = h.call(t, http.MethodPost, "/v1/users", with(pub, ptA), map[string]string{"nickname": "Minted"})
	var u struct {
		UserID string `json:"user_id"`
	}
	json.Unmarshal(body, &u)
	if resp, _ = h.call(t, http.MethodPatch, "/v1/users/"+u.UserID, with(pub, ptB), map[string]string{"nickname": "Nope"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("minted id bound to creator's token: %d", resp.StatusCode)
	}

	// switch on: publishable without token refused, secret without token allowed
	if resp, body = h.call(t, http.MethodPut, "/v1/apps/"+h.appID+"/player-tokens", h.sess(), map[string]bool{"require_player_tokens": true}); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"require_player_tokens":true`) {
		t.Fatalf("switch on: %d %s", resp.StatusCode, body)
	}
	if c, b := submit(pub, "surfer-3"); c != http.StatusUnauthorized || !strings.Contains(b, "player_token_required") {
		t.Fatalf("no token, switch on: %d %s", c, b)
	}
	if c, b := submit(h.key(), "surfer-3"); c >= 300 {
		t.Fatalf("secret key, no token, switch on: %d %s", c, b)
	}
	if resp, body = h.call(t, http.MethodGet, "/v1/apps/"+h.appID+"/player-tokens", h.sess(), nil); !strings.Contains(string(body), `"require_player_tokens":true`) {
		t.Fatalf("get switch: %d %s", resp.StatusCode, body)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `docker compose run --rm app go test ./pkg/api/ -run TestPlayerTokens -count=1`
Expected: FAIL at `other token: 202`, because tokens aren't checked yet.

- [ ] **Step 3: Implement**

`pkg/tenancy/store.go`: add to `App`:

```go
	// RequirePlayerTokens makes publishable-key writes for a player carry that
	// player's token (pkg/players). Off = migration window: tokens are bound
	// and checked when sent, but not required.
	RequirePlayerTokens bool `json:"require_player_tokens,omitempty"`
```

and to `Store`:

```go
	// SetRequirePlayerTokens toggles whether publishable-key player writes
	// must carry a player token, returning the updated app.
	SetRequirePlayerTokens(ctx context.Context, appID string, require bool) (App, error)
```

Redis: `return s.mutateApp(ctx, appID, func(a *App) { a.RequirePlayerTokens = require })`. Mem: copy `SetRequireSigning`'s body in `memstore.go`, setting `RequirePlayerTokens`.

`pkg/api/server.go`: add a `players players.Store` field to `Server`, set `players: players.NewMemStore()` in `NewServer`'s struct literal, and:

```go
// SetPlayers swaps the player-token store (cmd/leaderboardd wires Redis).
func (s *Server) SetPlayers(p players.Store) { s.players = p }

// authorizePlayer proves the caller may write as member. A token, when sent,
// is always checked (bound on first use) — also for secret keys, which is how
// a trusted backend passes a player's proof through. Without a token, secret
// keys and dashboard sessions pass; publishable keys pass only while the app
// has not turned on RequirePlayerTokens.
func (s *Server) authorizePlayer(w http.ResponseWriter, r *http.Request, app tenancy.App, member string) bool {
	token := strings.TrimSpace(r.Header.Get("X-Player-Token"))
	if token == "" {
		if keyScopeFrom(r.Context()) == tenancy.ScopeSecret || !app.RequirePlayerTokens {
			return true
		}
		writeErr(w, http.StatusUnauthorized, "player_token_required")
		return false
	}
	err := s.players.Authorize(r.Context(), app.ID, member, token)
	switch {
	case err == nil:
		return true
	case errors.Is(err, players.ErrInvalidToken):
		writeErr(w, http.StatusBadRequest, "invalid_player_token")
	case errors.Is(err, players.ErrTokenMismatch):
		writeErr(w, http.StatusForbidden, "player_token_mismatch")
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
	return false
}
```

In `handleSubmit`, right after the `req.Member == ""` check:

```go
	if !s.authorizePlayer(w, r, app, req.Member) {
		return
	}
```

`pkg/api/users.go`:
- `handleCreateUser`: replace the body after decoding with:

```go
	member := strings.TrimSpace(req.Member)
	token := strings.TrimSpace(r.Header.Get("X-Player-Token"))
	if member != "" {
		// claiming an existing member: prove it is yours (binds on first use)
		if !s.authorizePlayer(w, r, app, member) {
			return
		}
	} else if token != "" && !players.ValidToken(token) {
		// minting: reject a malformed token before creating anything
		writeErr(w, http.StatusBadRequest, "invalid_player_token")
		return
	} else if token == "" && keyScopeFrom(r.Context()) != tenancy.ScopeSecret && app.RequirePlayerTokens {
		writeErr(w, http.StatusUnauthorized, "player_token_required")
		return
	}
	u, err := s.users.Create(r.Context(), app.ID, req.Nickname, req.Member)
	if err != nil {
		writeUserErr(w, err)
		return
	}
	if member == "" && token != "" {
		// a minted plr_ id is brand new: bind the creator's token to it
		if err := s.players.Authorize(r.Context(), app.ID, u.ID, token); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusCreated, u)
```

  Add `"strings"` and the `players` import. Replace the `TODO(trust)` paragraph in the doc comment with: "Trust model: claims and minted ids are bound to the caller's player token (pkg/players); with a publishable key and `RequirePlayerTokens` on, a token is mandatory."
- `handleRenameUser`: before `Rename`, `if !s.authorizePlayer(w, r, app, r.PathValue("id")) { return }`.

`pkg/api/moderation.go` `handleDeleteUser`: before listing boards, `if !s.authorizePlayer(w, r, app, member) { return }`.

`pkg/api/auth.go`, next to the signing handlers:

```go
func (s *Server) handleGetPlayerTokens(w http.ResponseWriter, r *http.Request) {
	app, ok := s.ownedApp(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"require_player_tokens": app.RequirePlayerTokens})
}

func (s *Server) handleSetPlayerTokens(w http.ResponseWriter, r *http.Request) {
	app, ok := s.ownedApp(w, r)
	if !ok {
		return
	}
	var req struct {
		RequirePlayerTokens bool `json:"require_player_tokens"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	updated, err := s.store.SetRequirePlayerTokens(r.Context(), app.ID, req.RequirePlayerTokens)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"require_player_tokens": updated.RequirePlayerTokens})
}
```

Routes in `server.go`:

```go
	user("GET /v1/apps/{id}/player-tokens", s.handleGetPlayerTokens)
	user("PUT /v1/apps/{id}/player-tokens", s.handleSetPlayerTokens)
```

`cmd/leaderboardd/main.go`, after `srv := api.NewServer(…)`: `srv.SetPlayers(players.NewRedisStore(rdb))`.

- [ ] **Step 4: Run to verify it passes**

Run: `make test`
Expected: PASS (all packages, including the existing `TestUserEndpoints`, which sends no tokens with the secret key)

- [ ] **Step 5: Commit**

```bash
git add pkg cmd
git commit -m "feat(api): player tokens guard submit, claim, rename and delete; per-app require switch"
```

---

### Task 5: Dashboard — key scopes and the player-token switch

**Files:**
- Modify: `web/src/api.ts`, `web/src/pages/Dashboard.tsx`

**Interfaces:**
- Consumes: `POST /v1/apps/{id}/keys {scope}`, `GET …/keys` `scope`, `GET/PUT /v1/apps/{id}/player-tokens`

- [ ] **Step 1: API client**

In `web/src/api.ts`: add `scope?: "secret" | "publishable";` to `KeyInfo`. Replace `issueKey`:

```ts
  issueKey: (appId: string, scope: "secret" | "publishable" = "secret") =>
    req<{ id: string; prefix: string; api_key: string; scope: string }>("POST", `/v1/apps/${appId}/keys`, { scope }),
  getPlayerTokens: (appId: string) => req<{ require_player_tokens: boolean }>("GET", `/v1/apps/${appId}/player-tokens`),
  setPlayerTokens: (appId: string, require: boolean) =>
    req<{ require_player_tokens: boolean }>("PUT", `/v1/apps/${appId}/player-tokens`, { require_player_tokens: require }),
```

- [ ] **Step 2: Keys panel** (`Dashboard.tsx`, the component holding `issue()` around line 320)

Change `issue()` to `issue(scope: "secret" | "publishable")` and pass it to `api.issueKey(appId, scope)`. Replace the single "new key" button with two: `New secret key` → `issue("secret")` and `New publishable key` → `issue("publishable")`. In the key list row, show a label next to the prefix: `{k.scope === "publishable" ? "publishable · ship in clients" : "secret · servers only"}` using the existing `label`/`dim` classes. Under the buttons, add one `dim` paragraph: "Publishable keys go in game builds: reads and player writes only, each guarded by the player's token. Secret keys can create boards and moderate; keep them on servers."

- [ ] **Step 3: Player-token panel**

Add `function AppPlayerTokens({ appId }: { appId: string })` modelled on `AppSigning` (line 406): state `{ require_player_tokens: boolean } | null`, `load()` via `api.getPlayerTokens`, a `toggle(v)` via `api.setPlayerTokens`, and the same `panel` / `panel-head` / lamp markup. Label: "Require player tokens". Body text: "Publishable-key writes for a player (score submits, nickname claim, rename, delete) must carry that player's token. Turn on once most players run a build that sends tokens; older builds can still read." Render it right after `<AppSigning appId={appId} />` wherever that is used.

- [ ] **Step 4: Build**

Run: `cd web && npm run build`
Expected: build succeeds with no type errors.

- [ ] **Step 5: Manual check**

Run `docker compose up` and open the dashboard. Issue a publishable key, check the label shows, flip the switch and reload the page: it stays on.

- [ ] **Step 6: Commit**

```bash
git add web/src
git commit -m "feat(dashboard): publishable keys and the require-player-tokens switch"
```

---

### Task 6: SDK 0.7.0 — player tokens

**Files:**
- Modify: `sdk/typescript/src/index.ts`, `sdk/typescript/package.json` (version), `sdk/typescript/README.md`
- Test: `sdk/typescript/test/unit.mjs`

**Interfaces:**
- Produces: `ClientOptions.playerToken?: string | (() => string | undefined)`, `export class PlayerTokenError extends LeaderboardError { readonly code: "player_token_required" | "player_token_mismatch" | "invalid_player_token" }`

- [ ] **Step 1: Write the failing test** (append before `console.log("PASS: unit")`)

```js
// player token: sent as a header on every request, read through the getter at call time
let current = "t".repeat(64);
const tokCalls = [];
const tl = new LeaderboardClient("http://test", "key", {
  playerToken: () => current,
  fetch: (url, init) => {
    tokCalls.push(init);
    return Promise.resolve(new Response(JSON.stringify({ accepted: true }), { status: 202 }));
  },
});
await tl.submitScore("hs", "surfer-1", 5);
assert(tokCalls.at(-1).headers["X-Player-Token"] === "t".repeat(64), "token header sent");
current = "u".repeat(64);
await tl.deleteUser("surfer-1");
assert(tokCalls.at(-1).headers["X-Player-Token"] === "u".repeat(64), "getter read per call");
current = undefined;
await tl.getTop("hs", 5);
assert(!("X-Player-Token" in tokCalls.at(-1).headers), "no header without a token");

// token errors map to PlayerTokenError with the server's code
for (const [status, code] of [[403, "player_token_mismatch"], [401, "player_token_required"], [400, "invalid_player_token"]]) {
  const el = new LeaderboardClient("http://test", "key", {
    fetch: () => Promise.resolve(new Response(JSON.stringify({ error: code }), { status })),
  });
  let got = null;
  try {
    await el.submitScore("hs", "m", 1);
  } catch (e) {
    got = e;
  }
  assert(got instanceof PlayerTokenError && got.code === code && got.status === status, `${code} -> PlayerTokenError`);
}
```

Also add `PlayerTokenError` to the import at the top of `unit.mjs`.

- [ ] **Step 2: Run to verify it fails**

Run: `cd sdk/typescript && npm run build && npm run test:unit`
Expected: FAIL. `PlayerTokenError` isn't exported (import error), or the "token header sent" assert fails.

- [ ] **Step 3: Implement** (`src/index.ts`)

In `ClientOptions`:

```ts
  /**
   * The player's token (32–128 chars of [0-9a-zA-Z_-]), sent as
   * `X-Player-Token`. The server binds it to a member on first write and then
   * requires it for that member's writes. Pass a getter to swap tokens at run
   * time (e.g. after restoring an identity); it is read once per request.
   */
  playerToken?: string | (() => string | undefined);
```

Error class (after `MemberTakenError`):

```ts
/** A player-token failure: missing (401), wrong (403) or malformed (400). */
export class PlayerTokenError extends LeaderboardError {
  constructor(
    status: number,
    public readonly code: "player_token_required" | "player_token_mismatch" | "invalid_player_token",
    message: string,
  ) {
    super(status, message);
    this.name = "PlayerTokenError";
  }
}
```

In `send`, after building `headers`:

```ts
    const pt = typeof this.opts.playerToken === "function" ? this.opts.playerToken() : this.opts.playerToken;
    if (pt) headers["X-Player-Token"] = pt;
```

and before the generic `!resp.ok` throw:

```ts
    for (const code of ["player_token_required", "player_token_mismatch", "invalid_player_token"] as const) {
      if (!resp.ok && text.includes(code)) throw new PlayerTokenError(resp.status, code, text);
    }
```

(Check that `this.opts` is the field name the class uses for `ClientOptions`. `submitScore` uses `this.opts.signingSecret`, so it is.) Update the `registerUser` doc comment's trust caveat: with player tokens, only the token holder can claim a member.

`package.json`: `"version": "0.7.0"`. README: add a "Player tokens" section with the header, the bind-on-first-use rule, the error codes and the publishable/secret key split.

- [ ] **Step 4: Run to verify it passes**

Run: `cd sdk/typescript && npm run build && npm run test:unit && npm run test:hmac`
Expected: `PASS: unit` and the HMAC test passes.

- [ ] **Step 5: Commit** (do not publish; the owner runs `npm publish` from `sdk/typescript`)

```bash
git add sdk/typescript
git commit -m "feat(sdk): 0.7.0 — player tokens (X-Player-Token, PlayerTokenError)"
```

---

### Task 7: Docs and deploy notes

**Files:**
- Modify: `README.md` (trust / auth section)

- [ ] **Step 1:** In `README.md`'s auth or trust section, document:
  - the two key scopes and which routes are secret-only
  - `X-Player-Token`, bind-on-first-use, the three error codes
  - the `require_player_tokens` switch and its rollout order: deploy → issue a publishable key → ship clients → wait → turn on signing + player tokens → revoke old secret keys from clients
  - that bindings are never deleted (a deleted id can't be squatted)
- [ ] **Step 2:** Run `make test` and `cd web && npm run build` once more.
- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: key scopes and player tokens"
```

- [ ] **Step 4: Owner handoff.** List for the owner: deploy the server (the usual `deploy/` flow) and publish the SDK (`cd sdk/typescript && npm publish`). Nothing changes for existing apps until they issue publishable keys and flip the switch.
