# Users & Nicknames Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Per-app player registry — server-minted `plr_...` IDs with nicknames unique per app (case-insensitive), surfaced on all leaderboard reads.

**Architecture:** New `pkg/users` package (Store interface + mem/Redis impls, atomic Lua create/rename) mirroring `pkg/accounts`. API layer gets four `/v1/users` endpoints and best-effort nickname enrichment of read results via one pipelined `HMGET`. Engine stays untouched except a passive `Nickname` field on `RankEntry`. All three SDKs and the dashboard gain user methods + nickname display.

**Tech Stack:** Go 1.x, go-redis v9, net/http ServeMux (Go 1.22 patterns), React/TS dashboard, Unity C# / TypeScript SDKs.

**Spec:** `docs/superpowers/specs/2026-07-03-users-and-nicknames-design.md`

## Global Constraints

- Toolchain is Docker-only: run all Go commands via `docker compose run --rm app go ...` (or `make test`). There is no host Go or Redis. Redis-backed tests use `REDIS_ADDR` (defaults `localhost:6379`; inside compose the host is `redis:6379`) and `t.Skip` when unreachable — the compose `app` service has Redis available.
- Player ID prefix is `plr_` + 12 random hex bytes (NOT `usr_` — that prefix belongs to dashboard accounts in `pkg/accounts`).
- Redis keys: `plr:{<app>}:user:<id>` (JSON record), `plr:{<app>}:names` (HASH id→display nickname), `plr:{<app>}:nick` (HASH lowercase-nickname→id). The `{app}` hash tag is mandatory (single cluster slot → Lua atomicity).
- Nickname rules: trim whitespace; 1–32 runes after trim; no control characters; uniqueness key is `strings.ToLower(nickname)`.
- Stable API error strings: `invalid_nickname` (400), `nickname_taken` (409), `user_not_found` (404), rendered through the existing `writeErr` shape `{"error": "..."}`.
- The submit hot path (`handleSubmit`) must NOT change — unregistered members keep working (lenient mode).
- Module path is `github.com/kodeni-am/leaderboard`.
- Commit after each task with a short imperative message (matching `git log` style, e.g. "Add per-app player registry (pkg/users)").

## File Structure

| File | Responsibility |
|---|---|
| `pkg/users/store.go` (new) | `User` type, errors, `Store` interface, nickname validation, ID minting |
| `pkg/users/memstore.go` (new) | In-memory Store (tests, local runs) |
| `pkg/users/redisstore.go` (new) | Redis Store with atomic Lua create/rename |
| `pkg/users/users_test.go` (new) | Shared conformance suite + MemStore entry point |
| `pkg/users/redisstore_test.go` (new) | Redis entry point for the conformance suite |
| `pkg/api/users.go` (new) | `/v1/users` handlers + error mapping |
| `pkg/api/users_test.go` (new) | Endpoint + enrichment tests via the existing harness |
| `pkg/api/server.go` (modify) | Server field, NewServer param, routes, CORS PATCH, enrichment helper |
| `pkg/engine/engine.go` (modify) | Passive `Nickname` field on `RankEntry` |
| `cmd/leaderboardd/main.go` (modify) | Wire `users.NewRedisStore(rdb)` |
| `pkg/api/server_test.go` (modify) | Harness passes `users.NewMemStore()` |
| `pkg/sdk/client.go` + `client_test.go` (modify) | Go SDK user methods, `Nickname` on Entry |
| `sdk/typescript/src/index.ts` + `test/e2e.mjs` (modify) | TS SDK user methods + e2e coverage |
| `sdk/unity/Runtime/Models.cs` + `LeaderboardClient.cs` (modify) | Unity SDK user methods |
| `web/src/api.ts` + `web/src/pages/Dashboard.tsx` (modify) | Dashboard nickname display + register helper |
| `README.md` (modify) | API table + curl examples |

---

### Task 1: `pkg/users` core — types, validation, MemStore

**Files:**
- Create: `pkg/users/store.go`
- Create: `pkg/users/memstore.go`
- Test: `pkg/users/users_test.go`

**Interfaces:**
- Consumes: nothing (leaf package; stdlib only).
- Produces (later tasks depend on these exact names):
  - `type User struct { ID, Nickname string; CreatedAt, UpdatedAt time.Time }` with JSON tags `user_id`, `nickname`, `created_at`, `updated_at`
  - `type Store interface { Create(ctx, appID, nickname string) (User, error); Get(ctx, appID, id string) (User, error); GetByNickname(ctx, appID, nickname string) (User, error); Rename(ctx, appID, id, nickname string) (User, error); Nicknames(ctx, appID string, ids []string) (map[string]string, error) }`
  - `var ErrNotFound, ErrNicknameTaken, ErrInvalidNickname error`
  - `func NewMemStore() *MemStore`

- [ ] **Step 1: Write the failing conformance test**

Create `pkg/users/users_test.go`. `testStore` is the conformance suite Task 2's Redis store reuses — it takes the app id as a parameter so the Redis run can use a unique app per invocation (real Redis keeps state between runs).

```go
package users

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// testStore is the conformance suite every Store implementation must pass.
// app must be unique per run for stores with persistent backends.
func testStore(t *testing.T, s Store, app string) {
	ctx := context.Background()

	// Create trims whitespace, mints a plr_ id, and stores the display form.
	u, err := s.Create(ctx, app, "  Ninja  ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u.ID, "plr_") || u.Nickname != "Ninja" {
		t.Fatalf("unexpected user: %+v", u)
	}
	if u.CreatedAt.IsZero() || u.UpdatedAt.IsZero() {
		t.Errorf("timestamps not set: %+v", u)
	}
	if got, err := s.Get(ctx, app, u.ID); err != nil || got.Nickname != "Ninja" {
		t.Fatalf("Get: %+v / %v", got, err)
	}
	if got, err := s.GetByNickname(ctx, app, "NINJA"); err != nil || got.ID != u.ID {
		t.Fatalf("GetByNickname is case-insensitive: %+v / %v", got, err)
	}

	// Uniqueness is case-insensitive within an app, and scoped per app.
	if _, err := s.Create(ctx, app, "ninja"); !errors.Is(err, ErrNicknameTaken) {
		t.Errorf("case-insensitive dup: got %v, want ErrNicknameTaken", err)
	}
	if _, err := s.Create(ctx, app+"other", "Ninja"); err != nil {
		t.Errorf("same nick in another app: %v", err)
	}

	// Invalid nicknames are rejected before any state changes.
	for _, bad := range []string{"", "   ", strings.Repeat("x", 33), "a\x00b", "line\nbreak"} {
		if _, err := s.Create(ctx, app, bad); !errors.Is(err, ErrInvalidNickname) {
			t.Errorf("Create(%q): got %v, want ErrInvalidNickname", bad, err)
		}
	}
	// 32 runes of multibyte characters are valid (rune count, not bytes).
	if _, err := s.Create(ctx, app, strings.Repeat("ü", 32)); err != nil {
		t.Errorf("32-rune multibyte nickname: %v", err)
	}

	// Unknown lookups.
	if _, err := s.Get(ctx, app, "plr_nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get unknown: %v", err)
	}
	if _, err := s.GetByNickname(ctx, app, "Ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByNickname unknown: %v", err)
	}

	// Rename claims the new name and releases the old one.
	u2, err := s.Create(ctx, app, "Pixel")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rename(ctx, app, u2.ID, "Ninja"); !errors.Is(err, ErrNicknameTaken) {
		t.Errorf("rename to taken: %v", err)
	}
	if _, err := s.Rename(ctx, app, "plr_nope", "Foo"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rename unknown user: %v", err)
	}
	ren, err := s.Rename(ctx, app, u2.ID, "Voxel")
	if err != nil || ren.Nickname != "Voxel" {
		t.Fatalf("Rename: %+v / %v", ren, err)
	}
	if _, err := s.Create(ctx, app, "Pixel"); err != nil {
		t.Errorf("old name should be free after rename: %v", err)
	}
	if got, err := s.GetByNickname(ctx, app, "voxel"); err != nil || got.ID != u2.ID {
		t.Fatalf("new name resolves: %+v / %v", got, err)
	}
	// Case-only rename keeps the claim and updates the display form.
	if ren, err = s.Rename(ctx, app, u2.ID, "VOXEL"); err != nil || ren.Nickname != "VOXEL" {
		t.Fatalf("case-only rename: %+v / %v", ren, err)
	}

	// Batch nickname resolution skips unregistered ids.
	names, err := s.Nicknames(ctx, app, []string{u.ID, "raw-member", u2.ID})
	if err != nil {
		t.Fatal(err)
	}
	if names[u.ID] != "Ninja" || names[u2.ID] != "VOXEL" || len(names) != 2 {
		t.Fatalf("Nicknames: %v", names)
	}
	if names, err := s.Nicknames(ctx, app, nil); err != nil || len(names) != 0 {
		t.Fatalf("Nicknames(empty): %v / %v", names, err)
	}

	// Exactly one concurrent claimant can win a nickname.
	const claimants = 8
	errs := make(chan error, claimants)
	for i := 0; i < claimants; i++ {
		go func() {
			_, err := s.Create(ctx, app, "Contested")
			errs <- err
		}()
	}
	wins := 0
	for i := 0; i < claimants; i++ {
		if err := <-errs; err == nil {
			wins++
		} else if !errors.Is(err, ErrNicknameTaken) {
			t.Errorf("concurrent create: %v", err)
		}
	}
	if wins != 1 {
		t.Errorf("concurrent create: %d wins, want exactly 1", wins)
	}
}

func TestMemStore(t *testing.T) { testStore(t, NewMemStore(), "app_memtest") }
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `docker compose run --rm app go test ./pkg/users/ -count=1`
Expected: FAIL — compile error, package `users` does not exist yet.

- [ ] **Step 3: Write `pkg/users/store.go`**

```go
// Package users is the per-app player registry: server-minted player IDs
// (plr_...) with friendly nicknames that are unique per app,
// case-insensitively. It is separate from accounts (dashboard humans) — a
// users.User is a player inside a game. The player ID is the string games
// submit as the leaderboard member, so renames never touch board data.
package users

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrNotFound        = errors.New("users: user not found")
	ErrNicknameTaken   = errors.New("users: nickname already taken")
	ErrInvalidNickname = errors.New("users: nickname must be 1-32 characters with no control characters")
)

// User is a registered player within one app.
type User struct {
	ID        string    `json:"user_id"`
	Nickname  string    `json:"nickname"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Store persists players per app. Nickname uniqueness is enforced on the
// lowercased form; the display form is stored as entered.
type Store interface {
	// Create mints a plr_ id and claims nickname.
	// Returns ErrNicknameTaken or ErrInvalidNickname.
	Create(ctx context.Context, appID, nickname string) (User, error)
	// Get returns the player by id, or ErrNotFound.
	Get(ctx context.Context, appID, id string) (User, error)
	// GetByNickname resolves a nickname case-insensitively, or ErrNotFound.
	GetByNickname(ctx context.Context, appID, nickname string) (User, error)
	// Rename atomically claims the new nickname and releases the old one.
	Rename(ctx context.Context, appID, id, nickname string) (User, error)
	// Nicknames returns id -> display nickname for the ids that are
	// registered players; unregistered ids are simply absent from the map.
	Nicknames(ctx context.Context, appID string, ids []string) (map[string]string, error)
}

// normalizeNickname trims and validates nick, returning the display form and
// the lowercased uniqueness key.
func normalizeNickname(nick string) (display, lower string, err error) {
	display = strings.TrimSpace(nick)
	if display == "" || utf8.RuneCountInString(display) > 32 {
		return "", "", ErrInvalidNickname
	}
	for _, r := range display {
		if unicode.IsControl(r) {
			return "", "", ErrInvalidNickname
		}
	}
	return display, strings.ToLower(display), nil
}

// newID mints a player id ("plr_" + 12 random hex bytes). The prefix differs
// from accounts' usr_ so the two identity types are distinguishable.
func newID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "plr_" + hex.EncodeToString(b), nil
}
```

- [ ] **Step 4: Write `pkg/users/memstore.go`**

```go
package users

import (
	"context"
	"sync"
	"time"
)

// MemStore is the in-memory Store for tests and single-process local runs.
type MemStore struct {
	mu    sync.Mutex
	users map[string]map[string]User   // app -> id -> user
	nicks map[string]map[string]string // app -> lower(nick) -> id
}

func NewMemStore() *MemStore {
	return &MemStore{
		users: map[string]map[string]User{},
		nicks: map[string]map[string]string{},
	}
}

func (m *MemStore) Create(_ context.Context, appID, nickname string) (User, error) {
	display, lower, err := normalizeNickname(nickname)
	if err != nil {
		return User{}, err
	}
	id, err := newID()
	if err != nil {
		return User{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, taken := m.nicks[appID][lower]; taken {
		return User{}, ErrNicknameTaken
	}
	now := time.Now().UTC()
	u := User{ID: id, Nickname: display, CreatedAt: now, UpdatedAt: now}
	if m.users[appID] == nil {
		m.users[appID] = map[string]User{}
		m.nicks[appID] = map[string]string{}
	}
	m.users[appID][id] = u
	m.nicks[appID][lower] = id
	return u, nil
}

func (m *MemStore) Get(_ context.Context, appID, id string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[appID][id]
	if !ok {
		return User{}, ErrNotFound
	}
	return u, nil
}

func (m *MemStore) GetByNickname(_ context.Context, appID, nickname string) (User, error) {
	_, lower, err := normalizeNickname(nickname)
	if err != nil {
		return User{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.nicks[appID][lower]
	if !ok {
		return User{}, ErrNotFound
	}
	return m.users[appID][id], nil
}

func (m *MemStore) Rename(_ context.Context, appID, id, nickname string) (User, error) {
	display, lower, err := normalizeNickname(nickname)
	if err != nil {
		return User{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[appID][id]
	if !ok {
		return User{}, ErrNotFound
	}
	_, oldLower, _ := normalizeNickname(u.Nickname)
	if lower != oldLower {
		if _, taken := m.nicks[appID][lower]; taken {
			return User{}, ErrNicknameTaken
		}
		delete(m.nicks[appID], oldLower)
		m.nicks[appID][lower] = id
	}
	u.Nickname = display
	u.UpdatedAt = time.Now().UTC()
	m.users[appID][id] = u
	return u, nil
}

func (m *MemStore) Nicknames(_ context.Context, appID string, ids []string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if u, ok := m.users[appID][id]; ok {
			out[id] = u.Nickname
		}
	}
	return out, nil
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `docker compose run --rm app go test ./pkg/users/ -count=1 -v`
Expected: PASS (`TestMemStore`).

- [ ] **Step 6: Commit**

```bash
git add pkg/users/
git commit -m "Add per-app player registry core (pkg/users): types, validation, MemStore"
```

---

### Task 2: `pkg/users` RedisStore with atomic Lua create/rename

**Files:**
- Create: `pkg/users/redisstore.go`
- Test: `pkg/users/redisstore_test.go`

**Interfaces:**
- Consumes: Task 1's `Store`, `User`, errors, `normalizeNickname`, `newID`, and the `testStore` conformance suite.
- Produces: `func NewRedisStore(rdb redis.UniversalClient) *RedisStore` (used by `cmd/leaderboardd` in Task 3).

- [ ] **Step 1: Write the failing test**

Create `pkg/users/redisstore_test.go` — same Redis-availability skip pattern as `pkg/accounts/redisstore_test.go:13-26`; unique app id per run because Redis state persists.

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `docker compose run --rm app go test ./pkg/users/ -count=1 -run TestRedisStore`
Expected: FAIL — `NewRedisStore` undefined.

- [ ] **Step 3: Write `pkg/users/redisstore.go`**

```go
package users

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStore implements Store on Redis. All of an app's keys share a {app}
// hash tag (one cluster slot), which lets create/rename run as atomic Lua
// scripts — the invariant "no two players share a lowercased nickname" holds
// under concurrent claims.
type RedisStore struct {
	rdb redis.UniversalClient
}

func NewRedisStore(rdb redis.UniversalClient) *RedisStore { return &RedisStore{rdb: rdb} }

func playerKey(app, id string) string { return "plr:{" + app + "}:user:" + id }
func namesKey(app string) string      { return "plr:{" + app + "}:names" }
func nickKey(app string) string       { return "plr:{" + app + "}:nick" }

// createScript claims the lowercased nickname and writes the user record and
// the id->display mapping in one atomic step. Returns 0 if the name is taken.
// KEYS: 1=nick hash, 2=names hash, 3=user record
// ARGV: 1=lower nick, 2=id, 3=display nick, 4=user JSON
var createScript = redis.NewScript(`
if redis.call('HSETNX', KEYS[1], ARGV[1], ARGV[2]) == 0 then return 0 end
redis.call('HSET', KEYS[2], ARGV[2], ARGV[3])
redis.call('SET', KEYS[3], ARGV[4])
return 1
`)

// renameScript claims the new lowercased nickname, releases the old one (only
// if this player still owns it), and updates the record + display mapping. A
// case-only rename (same lower key) skips the claim and just updates display.
// KEYS: 1=nick hash, 2=names hash, 3=user record
// ARGV: 1=new lower, 2=old lower, 3=id, 4=new display, 5=user JSON
var renameScript = redis.NewScript(`
if ARGV[1] ~= ARGV[2] then
  if redis.call('HSETNX', KEYS[1], ARGV[1], ARGV[3]) == 0 then return 0 end
  if redis.call('HGET', KEYS[1], ARGV[2]) == ARGV[3] then
    redis.call('HDEL', KEYS[1], ARGV[2])
  end
end
redis.call('HSET', KEYS[2], ARGV[3], ARGV[4])
redis.call('SET', KEYS[3], ARGV[5])
return 1
`)

func (s *RedisStore) Create(ctx context.Context, appID, nickname string) (User, error) {
	display, lower, err := normalizeNickname(nickname)
	if err != nil {
		return User{}, err
	}
	id, err := newID()
	if err != nil {
		return User{}, err
	}
	now := time.Now().UTC()
	u := User{ID: id, Nickname: display, CreatedAt: now, UpdatedAt: now}
	data, err := json.Marshal(u)
	if err != nil {
		return User{}, err
	}
	ok, err := createScript.Run(ctx, s.rdb,
		[]string{nickKey(appID), namesKey(appID), playerKey(appID, id)},
		lower, id, display, data).Int()
	if err != nil {
		return User{}, err
	}
	if ok == 0 {
		return User{}, ErrNicknameTaken
	}
	return u, nil
}

func (s *RedisStore) Get(ctx context.Context, appID, id string) (User, error) {
	data, err := s.rdb.Get(ctx, playerKey(appID, id)).Bytes()
	if err == redis.Nil {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	var u User
	if err := json.Unmarshal(data, &u); err != nil {
		return User{}, err
	}
	return u, nil
}

func (s *RedisStore) GetByNickname(ctx context.Context, appID, nickname string) (User, error) {
	_, lower, err := normalizeNickname(nickname)
	if err != nil {
		return User{}, err
	}
	id, err := s.rdb.HGet(ctx, nickKey(appID), lower).Result()
	if err == redis.Nil {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return s.Get(ctx, appID, id)
}

func (s *RedisStore) Rename(ctx context.Context, appID, id, nickname string) (User, error) {
	display, lower, err := normalizeNickname(nickname)
	if err != nil {
		return User{}, err
	}
	u, err := s.Get(ctx, appID, id)
	if err != nil {
		return User{}, err
	}
	_, oldLower, err := normalizeNickname(u.Nickname)
	if err != nil {
		return User{}, err
	}
	u.Nickname = display
	u.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(u)
	if err != nil {
		return User{}, err
	}
	ok, err := renameScript.Run(ctx, s.rdb,
		[]string{nickKey(appID), namesKey(appID), playerKey(appID, id)},
		lower, oldLower, id, display, data).Int()
	if err != nil {
		return User{}, err
	}
	if ok == 0 {
		return User{}, ErrNicknameTaken
	}
	return u, nil
}

func (s *RedisStore) Nicknames(ctx context.Context, appID string, ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	vals, err := s.rdb.HMGet(ctx, namesKey(appID), ids...).Result()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(ids))
	for i, v := range vals {
		if name, ok := v.(string); ok {
			out[ids[i]] = name
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `docker compose run --rm app go test ./pkg/users/ -count=1 -v`
Expected: PASS (`TestMemStore`, `TestRedisStore`).

- [ ] **Step 5: Commit**

```bash
git add pkg/users/redisstore.go pkg/users/redisstore_test.go
git commit -m "Add Redis player store with atomic Lua nickname claims"
```

---

### Task 3: `/v1/users` API endpoints + server wiring

**Files:**
- Create: `pkg/api/users.go`
- Modify: `pkg/api/server.go` (struct field ~line 28-40, `NewServer` ~line 113, routes ~line 187-195, CORS header ~line 102)
- Modify: `cmd/leaderboardd/main.go` (~line 105)
- Modify: `pkg/api/server_test.go` (harness ~line 72-105)
- Modify: `pkg/sdk/client_test.go` (its `api.NewServer` call)
- Test: `pkg/api/users_test.go`

**Interfaces:**
- Consumes: `users.Store`, `users.NewMemStore`, `users.NewRedisStore`, users errors (Tasks 1-2); harness helpers `newHarness(t)`, `h.onboard(t, email)`, `h.call(t, method, path, headers, body)`, `h.key()` from `pkg/api/server_test.go`.
- Produces:
  - `NewServer(eng, ing, store, registry, acct, secureCookies, usrs)` — new final param `usrs users.Store` (every caller must be updated; verify with `grep -rn "api.NewServer\|NewServer(" pkg cmd`)
  - Routes: `POST /v1/users`, `GET /v1/users` (`?nickname=`), `GET /v1/users/{id}`, `PATCH /v1/users/{id}` — all on the data plane (`dataPlane(...)`)
  - `s.users` field, used by Task 4's enrichment.

- [ ] **Step 1: Write the failing endpoint test**

Create `pkg/api/users_test.go`:

```go
package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestUserEndpoints(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "users@example.com")

	// Register a player (API-key data plane).
	resp, body := h.call(t, http.MethodPost, "/v1/users", h.key(), map[string]string{"nickname": "Ninja"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: %d %s", resp.StatusCode, body)
	}
	var u struct {
		UserID   string `json:"user_id"`
		Nickname string `json:"nickname"`
	}
	json.Unmarshal(body, &u)
	if !strings.HasPrefix(u.UserID, "plr_") || u.Nickname != "Ninja" {
		t.Fatalf("register body: %s", body)
	}

	// Duplicate nickname (different case) -> 409 with a stable error code.
	resp, body = h.call(t, http.MethodPost, "/v1/users", h.key(), map[string]string{"nickname": "NINJA"})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "nickname_taken") {
		t.Fatalf("dup register: %d %s", resp.StatusCode, body)
	}

	// Invalid nickname -> 400.
	resp, body = h.call(t, http.MethodPost, "/v1/users", h.key(), map[string]string{"nickname": "  "})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "invalid_nickname") {
		t.Fatalf("invalid register: %d %s", resp.StatusCode, body)
	}

	// Get by id.
	resp, body = h.call(t, http.MethodGet, "/v1/users/"+u.UserID, h.key(), nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Ninja") {
		t.Fatalf("get: %d %s", resp.StatusCode, body)
	}
	resp, body = h.call(t, http.MethodGet, "/v1/users/plr_nope", h.key(), nil)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "user_not_found") {
		t.Fatalf("get unknown: %d %s", resp.StatusCode, body)
	}

	// Reverse lookup by nickname (case-insensitive); missing param -> 400.
	resp, body = h.call(t, http.MethodGet, "/v1/users?nickname=ninja", h.key(), nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), u.UserID) {
		t.Fatalf("lookup: %d %s", resp.StatusCode, body)
	}
	if resp, _ = h.call(t, http.MethodGet, "/v1/users", h.key(), nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("lookup without nickname: %d", resp.StatusCode)
	}

	// Rename; renaming to a taken name -> 409.
	resp, body = h.call(t, http.MethodPost, "/v1/users", h.key(), map[string]string{"nickname": "Pixel"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register 2nd: %d %s", resp.StatusCode, body)
	}
	var u2 struct {
		UserID string `json:"user_id"`
	}
	json.Unmarshal(body, &u2)
	resp, body = h.call(t, http.MethodPatch, "/v1/users/"+u2.UserID, h.key(), map[string]string{"nickname": "ninja"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("rename to taken: %d %s", resp.StatusCode, body)
	}
	resp, body = h.call(t, http.MethodPatch, "/v1/users/"+u2.UserID, h.key(), map[string]string{"nickname": "Voxel"})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Voxel") {
		t.Fatalf("rename: %d %s", resp.StatusCode, body)
	}
	// (Auth middleware behavior is covered by the existing data-plane tests;
	// the harness client carries a session cookie after onboard, so an
	// "unauthenticated" request here wouldn't actually be unauthenticated.)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `docker compose run --rm app go test ./pkg/api/ -count=1 -run TestUserEndpoints`
Expected: FAIL — compile error (handlers and `users` field don't exist).

- [ ] **Step 3: Create `pkg/api/users.go`**

```go
package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/kodeni-am/leaderboard/pkg/tenancy"
	"github.com/kodeni-am/leaderboard/pkg/users"
)

// Player registry endpoints (data plane). Registration is optional: raw
// member strings still work everywhere; registered players additionally get
// their nickname attached to read results.

type userReq struct {
	Nickname string `json:"nickname"`
}

// writeUserErr maps users store errors onto stable API error codes.
func writeUserErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, users.ErrInvalidNickname):
		writeErr(w, http.StatusBadRequest, "invalid_nickname")
	case errors.Is(err, users.ErrNicknameTaken):
		writeErr(w, http.StatusConflict, "nickname_taken")
	case errors.Is(err, users.ErrNotFound):
		writeErr(w, http.StatusNotFound, "user_not_found")
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	app, _ := tenancy.AppFromContext(r.Context())
	var req userReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "nickname required")
		return
	}
	u, err := s.users.Create(r.Context(), app.ID, req.Nickname)
	if err != nil {
		writeUserErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	app, _ := tenancy.AppFromContext(r.Context())
	u, err := s.users.Get(r.Context(), app.ID, r.PathValue("id"))
	if err != nil {
		writeUserErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleLookupUser(w http.ResponseWriter, r *http.Request) {
	app, _ := tenancy.AppFromContext(r.Context())
	nick := r.URL.Query().Get("nickname")
	if nick == "" {
		writeErr(w, http.StatusBadRequest, "nickname required")
		return
	}
	u, err := s.users.GetByNickname(r.Context(), app.ID, nick)
	if err != nil {
		writeUserErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleRenameUser(w http.ResponseWriter, r *http.Request) {
	app, _ := tenancy.AppFromContext(r.Context())
	var req userReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "nickname required")
		return
	}
	u, err := s.users.Rename(r.Context(), app.ID, r.PathValue("id"), req.Nickname)
	if err != nil {
		writeUserErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}
```

- [ ] **Step 4: Wire the store through `pkg/api/server.go`**

Four edits:

(a) Add the field to the `Server` struct (after `accounts`):

```go
	accounts      *accounts.Service
	users         users.Store
```

(b) Add the param to `NewServer` (line ~113) and import `github.com/kodeni-am/leaderboard/pkg/users`:

```go
func NewServer(eng engine.RankingEngine, ing *ingest.Ingestor, store tenancy.Store, registry *ingest.StaticRegistry, acct *accounts.Service, secureCookies bool, usrs users.Store) *Server {
	return &Server{eng: eng, ing: ing, store: store, registry: registry, accounts: acct, secureCookies: secureCookies, users: usrs}
}
```

(c) Register routes in `Handler()` after the `/v1/boards` data-plane block (line ~195):

```go
	// Player registry (optional; nicknames unique per app).
	dataPlane("POST /v1/users", s.handleCreateUser)
	dataPlane("GET /v1/users", s.handleLookupUser)
	dataPlane("GET /v1/users/{id}", s.handleGetUser)
	dataPlane("PATCH /v1/users/{id}", s.handleRenameUser)
```

(d) Add PATCH to the CORS preflight allowlist (line ~102):

```go
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
```

- [ ] **Step 5: Update every `NewServer` caller**

Find them all: `grep -rn "NewServer(" cmd pkg`

`cmd/leaderboardd/main.go` (~line 105-108) — create the Redis store and pass it:

```go
	rs := accounts.NewRedisStores(rdb)
	acctSvc := accounts.NewService(rs, rs, rs, buildMailer(), accounts.Config{BaseURL: publicURL})
	usrStore := users.NewRedisStore(rdb)

	srv := api.NewServer(eng, ing, store, registry, acctSvc, secureCk, usrStore)
```

Add `"github.com/kodeni-am/leaderboard/pkg/users"` to main.go's imports.

`pkg/api/server_test.go` harness (~line 93):

```go
	srv := NewServer(eng, ing, store, registry, acct, false, users.NewMemStore())
```

Add `"github.com/kodeni-am/leaderboard/pkg/users"` to server_test.go's imports.

`pkg/sdk/client_test.go` (~line 37):

```go
	srv := api.NewServer(eng, ing, store, registry, nil, false, users.NewMemStore())
```

Add `"github.com/kodeni-am/leaderboard/pkg/users"` to client_test.go's imports.

- [ ] **Step 6: Run the API tests, then the full suite**

Run: `docker compose run --rm app go test ./pkg/api/ -count=1 -run TestUserEndpoints -v`
Expected: PASS.

Run: `make test`
Expected: PASS everywhere (this catches any `NewServer` caller you missed).

- [ ] **Step 7: Commit**

```bash
git add pkg/api/ cmd/leaderboardd/main.go pkg/sdk/client_test.go
git commit -m "Add /v1/users endpoints: register, lookup, rename players"
```

---

### Task 4: Nickname enrichment on all read endpoints

**Files:**
- Modify: `pkg/engine/engine.go` (`RankEntry`, line ~31-36)
- Modify: `pkg/api/server.go` (`writeEntries` line ~541, `handleRank` line ~440, `handleTop`/`handlePage`/`handleNeighbors`/`handleFriends`)
- Test: `pkg/api/users_test.go` (append a test)

**Interfaces:**
- Consumes: `s.users.Nicknames` (Task 1), harness + `h.cons.Drain` (existing).
- Produces: `RankEntry.Nickname string` with tag `json:"nickname,omitempty"` — the field SDK/dashboard tasks (5-8) rely on.

- [ ] **Step 1: Write the failing enrichment test**

Append to `pkg/api/users_test.go`:

```go
func TestNicknameEnrichment(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "enrich@example.com")
	t.Cleanup(func() {
		_ = h.eng.Reset(context.Background(), engine.Board{Key: engine.BoardKey{App: h.appID, Board: "high", Segment: "all", Window: "all"}})
	})

	if resp, body := h.call(t, http.MethodPost, "/v1/boards", h.key(), map[string]any{"board": "high"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create board: %d %s", resp.StatusCode, body)
	}

	// One registered player, one raw member.
	resp, body := h.call(t, http.MethodPost, "/v1/users", h.key(), map[string]string{"nickname": "Ninja"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: %d %s", resp.StatusCode, body)
	}
	var u struct {
		UserID string `json:"user_id"`
	}
	json.Unmarshal(body, &u)

	for member, score := range map[string]float64{u.UserID: 500, "raw-anon": 300} {
		if resp, body := h.call(t, http.MethodPost, "/v1/boards/high/scores", h.key(), map[string]any{"member": member, "score": score}); resp.StatusCode != http.StatusAccepted {
			t.Fatalf("submit %s: %d %s", member, resp.StatusCode, body)
		}
	}
	if err := h.cons.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}

	// top: the registered member carries its nickname; the raw one omits it.
	resp, body = h.call(t, http.MethodGet, "/v1/boards/high/top?n=10", h.key(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("top: %d %s", resp.StatusCode, body)
	}
	var top struct {
		Entries []struct {
			Member   string `json:"member"`
			Nickname string `json:"nickname"`
		} `json:"entries"`
	}
	json.Unmarshal(body, &top)
	if len(top.Entries) != 2 || top.Entries[0].Member != u.UserID || top.Entries[0].Nickname != "Ninja" {
		t.Fatalf("top enrichment: %s", body)
	}
	if top.Entries[1].Nickname != "" || strings.Contains(jsonEntry(t, body, 1), `"nickname"`) {
		t.Fatalf("raw member should omit nickname: %s", body)
	}

	// rank (single entry) is enriched too.
	resp, body = h.call(t, http.MethodGet, "/v1/boards/high/rank?member="+u.UserID, h.key(), nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"nickname":"Ninja"`) {
		t.Fatalf("rank enrichment: %d %s", resp.StatusCode, body)
	}

	// neighbors is enriched.
	resp, body = h.call(t, http.MethodGet, "/v1/boards/high/neighbors?member="+u.UserID+"&k=2", h.key(), nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"nickname":"Ninja"`) {
		t.Fatalf("neighbors enrichment: %d %s", resp.StatusCode, body)
	}

	// friends is enriched.
	resp, body = h.call(t, http.MethodPost, "/v1/boards/high/friends", h.key(), map[string]any{"members": []string{u.UserID, "raw-anon"}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"nickname":"Ninja"`) {
		t.Fatalf("friends enrichment: %d %s", resp.StatusCode, body)
	}
}

// jsonEntry re-marshals entry i of an {"entries": [...]} body so tests can
// assert on the raw presence/absence of a key.
func jsonEntry(t *testing.T, body []byte, i int) string {
	t.Helper()
	var out struct {
		Entries []json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(body, &out); err != nil || len(out.Entries) <= i {
		t.Fatalf("jsonEntry: %v %s", err, body)
	}
	return string(out.Entries[i])
}
```

Add `"context"` and `"github.com/kodeni-am/leaderboard/pkg/engine"` to the file's imports.

- [ ] **Step 2: Run the test to verify it fails**

Run: `docker compose run --rm app go test ./pkg/api/ -count=1 -run TestNicknameEnrichment`
Expected: FAIL — entries have no `nickname` key.

- [ ] **Step 3: Add the field to `engine.RankEntry`**

In `pkg/engine/engine.go` (line ~31-36):

```go
// RankEntry is a member's position on a board.
type RankEntry struct {
	Member string  `json:"member"`
	Score  float64 `json:"score"` // decoded primary score
	Rank   int64   `json:"rank"`  // 1-based
	Exact  bool    `json:"exact"` // false only for the sharded approximate tier
	// Nickname is a friendly display name attached by the API layer from the
	// per-app player registry; the engine itself never populates it.
	Nickname string `json:"nickname,omitempty"`
}
```

- [ ] **Step 4: Enrich in `pkg/api/server.go`**

Add the helper next to `writeEntries`:

```go
// enrichEntries attaches registered nicknames to entries with one batched
// lookup. Best-effort: a failed lookup leaves entries unenriched rather than
// failing the read — names are auxiliary to ranks.
func (s *Server) enrichEntries(r *http.Request, entries []engine.RankEntry) {
	if len(entries) == 0 {
		return
	}
	app, _ := tenancy.AppFromContext(r.Context())
	ids := make([]string, len(entries))
	for i := range entries {
		ids[i] = entries[i].Member
	}
	names, err := s.users.Nicknames(r.Context(), app.ID, ids)
	if err != nil {
		return
	}
	for i := range entries {
		entries[i].Nickname = names[entries[i].Member]
	}
}
```

Change `writeEntries` to take the request and enrich:

```go
func (s *Server) writeEntries(w http.ResponseWriter, r *http.Request, entries []engine.RankEntry, err error) {
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.enrichEntries(r, entries)
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}
```

Update its four callers to pass `r`:
- `handleTop` (line ~480): `s.writeEntries(w, r, entries, err)`
- `handlePage` (line ~489): `s.writeEntries(w, r, entries, err)`
- `handleNeighbors` (line ~507): `s.writeEntries(w, r, entries, err)`
- `handleFriends` (line ~525): `s.writeEntries(w, r, entries, err)`

In `handleRank`, replace the final `writeJSON(w, http.StatusOK, entry)` with:

```go
	enriched := []engine.RankEntry{entry}
	s.enrichEntries(r, enriched)
	writeJSON(w, http.StatusOK, enriched[0])
```

- [ ] **Step 5: Run the tests**

Run: `docker compose run --rm app go test ./pkg/api/ ./pkg/engine/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/engine/engine.go pkg/api/
git commit -m "Enrich leaderboard reads with registered nicknames"
```

---

### Task 5: Go SDK user methods

**Files:**
- Modify: `pkg/sdk/client.go`
- Test: `pkg/sdk/client_test.go`

**Interfaces:**
- Consumes: `/v1/users` endpoints (Task 3), `nickname` in read entries (Task 4).
- Produces: `sdk.User{UserID, Nickname, CreatedAt, UpdatedAt}`, `sdk.ErrNicknameTaken`, `Client.RegisterUser/GetUser/UserByNickname/RenameUser`, `Entry.Nickname`.

- [ ] **Step 1: Write the failing test**

Append to `pkg/sdk/client_test.go` (inside the file; reuse its setup style — this is a new test function with its own server):

```go
func TestSDKUsers(t *testing.T) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewUniversalClient(&redis.UniversalOptions{Addrs: []string{addr}})
	pctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(pctx).Err(); err != nil {
		t.Skipf("redis not available: %v", err)
	}
	ctx := context.Background()

	eng := engine.NewRedisEngine(rdb)
	store := tenancy.NewMemStore()
	registry := ingest.NewStaticRegistry()
	log := ingest.NewMemLog()
	ing := ingest.NewIngestor(log, registry, ingest.NewMemDeduper())
	cons := ingest.NewConsumer(log, registry, eng)
	srv := api.NewServer(eng, ing, store, registry, nil, false, users.NewMemStore())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	app, key, err := store.CreateApp(ctx, "usr_sdk_test", "Racer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = eng.Reset(ctx, engine.Board{Key: engine.BoardKey{App: app.ID, Board: "high", Segment: "all", Window: "all"}})
	})
	c := New(ts.URL, key)
	if err := c.CreateBoard(ctx, BoardDef{Board: "high"}); err != nil {
		t.Fatal(err)
	}

	// Register + duplicate -> ErrNicknameTaken.
	u, err := c.RegisterUser(ctx, "Ninja")
	if err != nil || !strings.HasPrefix(u.UserID, "plr_") || u.Nickname != "Ninja" {
		t.Fatalf("RegisterUser: %+v / %v", u, err)
	}
	if _, err := c.RegisterUser(ctx, "ninja"); !errors.Is(err, ErrNicknameTaken) {
		t.Errorf("dup register: %v", err)
	}

	// Lookup both ways.
	if got, err := c.GetUser(ctx, u.UserID); err != nil || got.Nickname != "Ninja" {
		t.Fatalf("GetUser: %+v / %v", got, err)
	}
	if got, err := c.UserByNickname(ctx, "NINJA"); err != nil || got.UserID != u.UserID {
		t.Fatalf("UserByNickname: %+v / %v", got, err)
	}
	if _, err := c.GetUser(ctx, "plr_nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetUser unknown: %v", err)
	}

	// Rename.
	if ren, err := c.RenameUser(ctx, u.UserID, "Shadow"); err != nil || ren.Nickname != "Shadow" {
		t.Fatalf("RenameUser: %+v / %v", ren, err)
	}

	// Read enrichment: submit as the player, drain, and read the nickname back.
	if _, err := c.Submit(ctx, "high", Submission{Member: u.UserID, Score: 900}); err != nil {
		t.Fatal(err)
	}
	if err := cons.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	top, err := c.Top(ctx, "high", 10, QueryOpts{})
	if err != nil || len(top) != 1 || top[0].Nickname != "Shadow" {
		t.Fatalf("Top with nickname: %+v / %v", top, err)
	}
}
```

Add `"strings"` and `"github.com/kodeni-am/leaderboard/pkg/users"` to the file's imports if missing.

- [ ] **Step 2: Run the test to verify it fails**

Run: `docker compose run --rm app go test ./pkg/sdk/ -count=1 -run TestSDKUsers`
Expected: FAIL — `RegisterUser` undefined.

- [ ] **Step 3: Implement in `pkg/sdk/client.go`**

Add `Nickname` to `Entry` (line ~22-27):

```go
type Entry struct {
	Member   string  `json:"member"`
	Score    float64 `json:"score"`
	Rank     int64   `json:"rank"`
	Exact    bool    `json:"exact"`
	Nickname string  `json:"nickname,omitempty"` // set for registered players
}
```

Add next to `ErrNotFound` (line ~19):

```go
// ErrNicknameTaken is returned when a nickname is already claimed in the app.
var ErrNicknameTaken = errors.New("sdk: nickname already taken")
```

In `do()`, map 409 right after the 404 mapping (line ~101-103):

```go
	if resp.StatusCode == http.StatusConflict {
		return resp.StatusCode, ErrNicknameTaken
	}
```

Append the user methods at the end of the file:

```go
// User is a registered player: a server-minted ID plus a nickname unique
// within the app (case-insensitively). Submit scores with UserID as the
// member and leaderboard reads return the nickname alongside it.
type User struct {
	UserID    string    `json:"user_id"`
	Nickname  string    `json:"nickname"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RegisterUser mints a player and claims nickname. Returns ErrNicknameTaken
// if the name is already claimed in this app.
func (c *Client) RegisterUser(ctx context.Context, nickname string) (User, error) {
	var u User
	_, err := c.do(ctx, http.MethodPost, "/v1/users", map[string]string{"nickname": nickname}, &u)
	return u, err
}

// GetUser fetches a registered player by id. Returns ErrNotFound if absent.
func (c *Client) GetUser(ctx context.Context, id string) (User, error) {
	var u User
	_, err := c.do(ctx, http.MethodGet, "/v1/users/"+url.PathEscape(id), nil, &u)
	return u, err
}

// UserByNickname resolves a nickname (case-insensitive) to its player.
func (c *Client) UserByNickname(ctx context.Context, nickname string) (User, error) {
	var u User
	_, err := c.do(ctx, http.MethodGet, "/v1/users?nickname="+url.QueryEscape(nickname), nil, &u)
	return u, err
}

// RenameUser changes a player's nickname. Returns ErrNicknameTaken on
// conflict. The player id (and therefore board data and HMAC signatures) is
// unaffected by renames.
func (c *Client) RenameUser(ctx context.Context, id, nickname string) (User, error) {
	var u User
	_, err := c.do(ctx, http.MethodPatch, "/v1/users/"+url.PathEscape(id), map[string]string{"nickname": nickname}, &u)
	return u, err
}
```

- [ ] **Step 4: Run the tests**

Run: `docker compose run --rm app go test ./pkg/sdk/ -count=1 -v`
Expected: PASS (both `TestSDKAgainstServer` and `TestSDKUsers`).

- [ ] **Step 5: Commit**

```bash
git add pkg/sdk/
git commit -m "Go SDK: user registration, lookup, rename; nickname on entries"
```

---

### Task 6: TypeScript SDK user methods

**Files:**
- Modify: `sdk/typescript/src/index.ts`
- Modify: `sdk/typescript/test/e2e.mjs`

**Interfaces:**
- Consumes: `/v1/users` endpoints, `nickname` in entries.
- Produces: `User` interface, `NicknameTakenError`, `registerUser/getUser/getUserByNickname/renameUser` methods, `nickname?` on `RankEntry`.

- [ ] **Step 1: Extend `src/index.ts`**

Add `nickname` to `RankEntry` (line ~15-20):

```ts
export interface RankEntry {
  member: string;
  score: number;
  rank: number; // 1-based
  exact: boolean; // false only for the sharded approximate tier
  nickname?: string; // present when the member is a registered player
}
```

Add after the `RankEntry` interface:

```ts
/** A registered player: server-minted id + nickname unique per app (case-insensitive). */
export interface User {
  user_id: string;
  nickname: string;
  created_at?: string;
  updated_at?: string;
}
```

Add after `NotFoundError` (line ~90-95):

```ts
/** Thrown when a nickname is already claimed in this app (HTTP 409). */
export class NicknameTakenError extends LeaderboardError {
  constructor(message: string) {
    super(409, message);
    this.name = "NicknameTakenError";
  }
}
```

In `send()` (line ~193-205), map 409 right after the 404 line:

```ts
    if (resp.status === 404) throw new NotFoundError(text);
    if (resp.status === 409) throw new NicknameTakenError(text);
```

Add methods to `LeaderboardClient` (after `getFriends`):

```ts
  /**
   * Register a player: mints a `plr_...` user id and claims a nickname
   * (unique per app, case-insensitive). Submit scores with `user_id` as the
   * member; reads then include the nickname. Throws {@link NicknameTakenError}
   * if the name is claimed.
   */
  async registerUser(nickname: string): Promise<User> {
    return this.send("POST", "/v1/users", { nickname });
  }

  /** Fetch a registered player by id. Throws {@link NotFoundError} if absent. */
  async getUser(userId: string): Promise<User> {
    return this.send("GET", `/v1/users/${enc(userId)}`);
  }

  /** Resolve a nickname (case-insensitive) to its player. */
  async getUserByNickname(nickname: string): Promise<User> {
    return this.send("GET", `/v1/users${qs({ nickname })}`);
  }

  /**
   * Change a player's nickname. The user id — and therefore board data and
   * HMAC signatures — is unaffected. Throws {@link NicknameTakenError} on
   * conflict.
   */
  async renameUser(userId: string, nickname: string): Promise<User> {
    return this.send("PATCH", `/v1/users/${enc(userId)}`, { nickname });
  }
```

- [ ] **Step 2: Typecheck**

Run: `cd sdk/typescript && npm run typecheck`
Expected: clean exit. (Node runs on the host for the TS SDK — it has no Go/Redis dependency. If npm is unavailable, note it and continue; CI covers it.)

- [ ] **Step 3: Extend `test/e2e.mjs`**

Update the import line and append before the final `console.log`:

```js
import { LeaderboardClient, NotFoundError, NicknameTakenError } from "../dist/index.js";
```

```js
// Users & nicknames.
const nick = `Tester-${Date.now()}`;
const u = await lb.registerUser(nick);
assert(u.user_id.startsWith("plr_") && u.nickname === nick, `registerUser ${JSON.stringify(u)}`);

let conflict = false;
try {
  await lb.registerUser(nick.toUpperCase());
} catch (e) {
  conflict = e instanceof NicknameTakenError;
}
assert(conflict, "duplicate nickname throws NicknameTakenError");

const byNick = await lb.getUserByNickname(nick.toLowerCase());
assert(byNick.user_id === u.user_id, "getUserByNickname resolves case-insensitively");

const renamed = await lb.renameUser(u.user_id, `${nick}-2`);
assert(renamed.nickname === `${nick}-2`, "renameUser");

await lb.submitScore("high", u.user_id, 999);
await new Promise((res) => setTimeout(res, 1500));
const enriched = await lb.getTop("high", 5);
const mine = enriched.find((e) => e.member === u.user_id);
assert(mine && mine.nickname === `${nick}-2`, `top carries nickname ${JSON.stringify(enriched)}`);
```

- [ ] **Step 4: Run the e2e test if a server is up (optional gate)**

Run: `docker compose up -d leaderboardd && cd sdk/typescript && npm run build && LB_API_KEY=<key from dashboard> npm run test:e2e`
Expected: `TS SDK e2e: PASS ✅`. The script self-skips without `LB_API_KEY`; typecheck in Step 2 is the required gate.

- [ ] **Step 5: Commit**

```bash
git add sdk/typescript/
git commit -m "TS SDK: user registration, lookup, rename; nickname on entries"
```

---

### Task 7: Unity SDK user methods

**Files:**
- Modify: `sdk/unity/Runtime/Models.cs`
- Modify: `sdk/unity/Runtime/LeaderboardClient.cs`

**Interfaces:**
- Consumes: `/v1/users` endpoints.
- Produces: `UserInfo`, `NicknameTakenException`, `RegisterUserAsync/GetUserAsync/GetUserByNicknameAsync/RenameUserAsync`, `RankEntry.nickname`.

There is no Unity test runner in this repo; the gate is that `make test` still passes (no Go coupling) and the C# mirrors the wire format exactly (JsonUtility requires field names to match JSON keys).

- [ ] **Step 1: Extend `Models.cs`**

Add `nickname` to `RankEntry` (line ~11-17):

```csharp
    [Serializable]
    public class RankEntry
    {
        public string member;
        public double score;
        public long rank; // 1-based
        public bool exact; // false only for the sharded approximate tier
        public string nickname; // null/empty unless the member is a registered player
    }
```

Add after `NotFoundException` (line ~36-40):

```csharp
    /// <summary>Raised when a nickname is already claimed in this app (HTTP 409).</summary>
    public class NicknameTakenException : LeaderboardException
    {
        public NicknameTakenException(string message) : base(409, message) { }
    }

    /// <summary>A registered player: server-minted id + per-app-unique nickname.</summary>
    [Serializable]
    public class UserInfo
    {
        public string user_id;
        public string nickname;
    }
```

Add to the internal wire DTO section:

```csharp
    [Serializable]
    internal class UserRequest
    {
        public string nickname;
    }
```

- [ ] **Step 2: Extend `LeaderboardClient.cs`**

In `SendAsync` (line ~141-168), add the 409 mapping right after the 404 line:

```csharp
                if (code == 404) throw new NotFoundException(text);
                if (code == 409) throw new NicknameTakenException(text);
```

Add methods after `GetFriendsAsync` (line ~102):

```csharp
        /// <summary>
        /// Register a player: mints a user id (plr_...) and claims a nickname,
        /// which is unique per app (case-insensitive). Submit scores with the
        /// returned user_id as the member; reads then include the nickname.
        /// Throws <see cref="NicknameTakenException"/> if the name is claimed.
        /// </summary>
        public async Task<UserInfo> RegisterUserAsync(string nickname)
        {
            var body = new UserRequest { nickname = nickname };
            string resp = await SendAsync("POST", "/v1/users", UnityEngine.JsonUtility.ToJson(body));
            return UnityEngine.JsonUtility.FromJson<UserInfo>(resp);
        }

        /// <summary>Fetch a registered player by id. Throws <see cref="NotFoundException"/> if absent.</summary>
        public async Task<UserInfo> GetUserAsync(string userId)
        {
            string resp = await SendAsync("GET", "/v1/users/" + Esc(userId), null);
            return UnityEngine.JsonUtility.FromJson<UserInfo>(resp);
        }

        /// <summary>Resolve a nickname (case-insensitive) to its player.</summary>
        public async Task<UserInfo> GetUserByNicknameAsync(string nickname)
        {
            string resp = await SendAsync("GET", "/v1/users?nickname=" + Esc(nickname), null);
            return UnityEngine.JsonUtility.FromJson<UserInfo>(resp);
        }

        /// <summary>
        /// Change a player's nickname. The user id (and any HMAC signatures over
        /// it) is unaffected. Throws <see cref="NicknameTakenException"/> on conflict.
        /// </summary>
        public async Task<UserInfo> RenameUserAsync(string userId, string nickname)
        {
            var body = new UserRequest { nickname = nickname };
            string resp = await SendAsync("PATCH", "/v1/users/" + Esc(userId), UnityEngine.JsonUtility.ToJson(body));
            return UnityEngine.JsonUtility.FromJson<UserInfo>(resp);
        }
```

- [ ] **Step 3: Verify the wire format matches**

Cross-check field names against the API: `user_id`, `nickname` (Task 3's `users.User` JSON tags). JsonUtility matches by exact field name — any mismatch silently deserializes to null, so re-read the Go struct tags and confirm.

- [ ] **Step 4: Commit**

```bash
git add sdk/unity/
git commit -m "Unity SDK: user registration, lookup, rename; nickname on entries"
```

---

### Task 8: Dashboard — nickname display + register helper

**Files:**
- Modify: `web/src/api.ts`
- Modify: `web/src/pages/Dashboard.tsx` (`Viewer` table line ~583-596, `TestSubmit` line ~602-623, `RankSearch` line ~648-654)

**Interfaces:**
- Consumes: `/v1/users` endpoints via session auth + `X-App-Id` (the `req` helper and `appHdr` already handle CSRF/cookies), `nickname` on entries.
- Produces: `api.registerUser`, `RankEntry.nickname?` — used only within the dashboard.

- [ ] **Step 1: Extend `web/src/api.ts`**

Add `nickname` to `RankEntry` (line ~26-31):

```ts
export interface RankEntry {
  member: string;
  score: number;
  rank: number;
  exact: boolean;
  nickname?: string;
}
```

Add a `UserInfo` interface next to the others:

```ts
export interface UserInfo {
  user_id: string;
  nickname: string;
}
```

Add to the `api` object (after `submit`):

```ts
  registerUser: (appId: string, nickname: string) =>
    req<UserInfo>("POST", "/v1/users", { nickname }, appHdr(appId)),
```

- [ ] **Step 2: Show nicknames in the Viewer table (`Dashboard.tsx` line ~585-593)**

```tsx
            <thead><tr><th>Rank</th><th>Player</th><th style={{ textAlign: "right" }}>Score</th></tr></thead>
            <tbody>
              {entries.map((e) => (
                <tr key={e.member}>
                  <td className="rank">{String(e.rank).padStart(2, "0")}</td>
                  <td>
                    {e.nickname
                      ? <>{e.nickname} <span className="dim mono" style={{ fontSize: 12 }}>{e.member}</span></>
                      : <span className="mono">{e.member}</span>}
                  </td>
                  <td className="score">{e.score.toLocaleString()}</td>
                </tr>
              ))}
            </tbody>
```

- [ ] **Step 3: Add a register helper to `TestSubmit` (line ~602-623)**

Replace the component with:

```tsx
function TestSubmit({ appId, board, segment, busy, onSubmit }: { appId: string; board: string; segment: string; busy: boolean; onSubmit: (m: string, s: number) => void }) {
  const [member, setMember] = useState("");
  const [score, setScore] = useState("");
  const [nickname, setNickname] = useState("");
  const [regMsg, setRegMsg] = useState("");
  const dest = `${board} · all windows · ${segment || "all"} segment`;

  // Registers a player and drops the minted id into the member field, so a
  // test submit exercises the nickname-enriched path.
  async function register() {
    if (!nickname) return;
    try {
      const u = await api.registerUser(appId, nickname);
      setMember(u.user_id);
      setRegMsg(`${u.nickname} → ${u.user_id}`);
    } catch (e) {
      setRegMsg((e as ApiError).status === 409 ? "Nickname taken — try another." : (e as ApiError).message);
    }
  }

  return (
    <div className="stack-sm">
      <form
        className="panel row collapse"
        style={{ gap: 10, alignItems: "flex-end" }}
        onSubmit={(e) => { e.preventDefault(); if (member && score !== "") onSubmit(member, Number(score)); }}
      >
        <label className="field" style={{ margin: 0, flex: 1 }}>
          <span>Member</span>
          <input value={member} onChange={(e) => setMember(e.target.value)} placeholder="player-1 or plr_…" />
        </label>
        <label className="field" style={{ margin: 0, width: 130 }}>
          <span>Score</span>
          <input value={score} onChange={(e) => setScore(e.target.value)} placeholder="1500" type="number" />
        </label>
        <button className="btn" type="submit" disabled={busy} title={`Submit to ${dest}`}>{busy ? "…" : "Submit"}</button>
      </form>
      <form
        className="panel row collapse"
        style={{ gap: 10, alignItems: "flex-end" }}
        onSubmit={(e) => { e.preventDefault(); void register(); }}
      >
        <label className="field" style={{ margin: 0, flex: 1 }}>
          <span>Register a player (nickname → member id)</span>
          <input value={nickname} onChange={(e) => setNickname(e.target.value)} placeholder="Ninja" />
        </label>
        <button className="btn btn-ghost" type="submit">Register</button>
        <div className="mono dim" style={{ minWidth: 150, textAlign: "right", fontSize: 12 }}>{regMsg}</div>
      </form>
    </div>
  );
}
```

(Note the `appId` prop is now used — remove any unused-var suppression if present.)

- [ ] **Step 4: Show the nickname in `RankSearch` results (line ~648-654)**

```tsx
      <div className="mono" style={{ minWidth: 150, textAlign: "right", fontSize: 14 }}>
        {result ? (
          <span>{result.nickname ? `${result.nickname} · ` : ""}<span className="accent">#{result.rank}</span> · {result.score.toLocaleString()}</span>
        ) : (
          <span className="dim">{msg}</span>
        )}
      </div>
```

- [ ] **Step 5: Build the web app to verify**

Run: `cd web && npm run build` (or the project's compose equivalent if the host lacks node — check `web/package.json` scripts).
Expected: clean TypeScript build, no unused-variable errors.

- [ ] **Step 6: Commit**

```bash
git add web/src/
git commit -m "Dashboard: show player nicknames; register-player test helper"
```

---

### Task 9: Docs + full verification

**Files:**
- Modify: `README.md` (API table line ~110-124, curl examples line ~92-104)

**Interfaces:** none — documentation and final gate.

- [ ] **Step 1: Extend the README API table**

After the `POST /v1/boards/{board}/friends` row (line ~123), add:

```markdown
| `POST /v1/users` | Register a player (server-minted id + nickname, unique per app) |
| `GET /v1/users/{id}` · `GET /v1/users?nickname=` | Fetch / resolve a player |
| `PATCH /v1/users/{id}` | Rename a player (id and board data unaffected) |
```

- [ ] **Step 2: Add a curl example + note**

After the existing read examples (line ~104):

```bash
# Optional player registry: register once, then submit with the minted id.
curl -s -X POST $BASE/v1/users -H "Authorization: Bearer $KEY" \
  -d '{"nickname":"Ninja"}'                    # -> {"user_id":"plr_...","nickname":"Ninja",...}
curl -s -X POST $BASE/v1/boards/high/scores -H "Authorization: Bearer $KEY" \
  -d '{"member":"plr_...","score":1500}'
```

And after the "All query endpoints accept…" paragraph, add:

```markdown
Read entries include a `nickname` field for members registered via
`/v1/users`; raw (unregistered) member strings keep working and simply omit
it. Nicknames are unique per app, case-insensitively; renames are O(1) and
never touch board data.
```

- [ ] **Step 3: Run the full suite**

Run: `make test`
Expected: all packages PASS.

Run: `make vet`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "Document the player registry and nickname enrichment"
```
