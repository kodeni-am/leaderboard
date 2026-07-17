# Player Counts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show two counts in the dashboard — how many players are on the board as currently filtered (`TOP 25 OF 1,432`), and how many players are registered in the app (`5,204 players` beside the app selector).

**Architecture:** `engine.Count` already exists on the `RankingEngine` interface with exact implementations on both engines, so board depth is purely new HTTP/client/UI layers. The app-wide count needs one new `users.Store.Count` method, backed by `HLEN` on the existing names hash. Two new endpoints: `GET /v1/boards/{board}/count` (data plane, honors `window`/`segment`) and `GET /v1/apps/{id}/stats` (owner plane).

**Tech Stack:** Go 1.x + Redis (go-redis v9), React + TypeScript (Vite), Docker Compose toolchain.

**Spec:** `docs/superpowers/specs/2026-07-17-player-counts-design.md`

## Global Constraints

- **Toolchain is Docker-only.** There is no host Go or Redis. Every Go command runs via compose: `docker compose run --rm app go ...`. Full suite is `make test`.
- **No engine changes.** `RankingEngine.Count(ctx context.Context, b Board) (int64, error)` already exists (`pkg/engine/engine.go:69`) with implementations in `redis_engine.go:385` (`ZCARD`) and `sharded_engine.go:122` (sums per-shard `ZCARD`s). Do not add or modify engine methods.
- **Counts are auxiliary.** A count failure must never break a leaderboard render or surface an error banner — follow the `enrichEntries` best-effort precedent (`pkg/api/server.go:579`).
- **JSON field names are exact:** board count responds `{"count": N}`; app stats responds `{"players": N}`.
- **Do not add SDK methods** (Go `pkg/sdk` or TS). Explicitly out of scope.
- Web verification is `npm --prefix web run build` — the project has no JS test runner.

## File Structure

| File | Responsibility | Task |
|---|---|---|
| `pkg/users/store.go` | `Store` interface — add `Count` | 1 |
| `pkg/users/memstore.go` | `MemStore.Count` — `len(map)` under lock | 1 |
| `pkg/users/redisstore.go` | `RedisStore.Count` — `HLEN` names hash | 1 |
| `pkg/users/users_test.go` | `testStore` conformance suite — count cases | 1 |
| `pkg/api/server.go` | Route + `handleCount` (board depth) | 2 |
| `pkg/api/count_test.go` | **Create** — both endpoints' tests | 2, 3 |
| `pkg/api/auth.go` | `handleAppStats` (owner plane, beside signing handlers) | 3 |
| `README.md` | API table rows | 2, 3 |
| `web/src/api.ts` | `count()`, `appStats()`, `qsFirst()` helper | 4, 5 |
| `web/src/pages/Dashboard.tsx` | Viewer header count; Dashboard playerbase count | 4, 5 |

---

### Task 1: `users.Store.Count`

**Files:**
- Modify: `pkg/users/store.go:48-71` (the `Store` interface)
- Modify: `pkg/users/memstore.go` (append method)
- Modify: `pkg/users/redisstore.go` (append method)
- Test: `pkg/users/users_test.go` (append to `testStore`, before its closing brace at ~line 302)

**Interfaces:**
- Consumes: nothing (first task).
- Produces: `users.Store.Count(ctx context.Context, appID string) (int64, error)` — returns the number of registered players in the app; `0, nil` for an unknown app. Task 3 calls this as `s.users.Count(r.Context(), app.ID)`.

**Context the implementer needs:** `testStore` (`pkg/users/users_test.go:13`) is the conformance suite both stores run — `TestMemStore` (line 304) and `TestRedisStore` (`redisstore_test.go:24`). It accumulates users in the shared `app` across its sections, so the count cases below use **their own app namespace** (`app+"cnt"`). Do not count the shared `app` — that would couple the expected numbers to suite ordering and break the next time a case is added above.

- [ ] **Step 1: Write the failing test**

Append to `pkg/users/users_test.go`, at the end of `testStore` immediately before its closing brace (after the concurrent member-claim section):

```go
	// Count reports the app's registered players. This runs in its own app
	// namespace so the expected numbers don't depend on what the sections
	// above created in the shared app.
	cntApp := app + "cnt"
	if n, err := s.Count(ctx, cntApp); err != nil || n != 0 {
		t.Fatalf("fresh app: count %d / %v, want 0", n, err)
	}
	alpha, err := s.Create(ctx, cntApp, "Alpha", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, cntApp, "Beta", ""); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Count(ctx, cntApp); err != nil || n != 2 {
		t.Fatalf("after 2 creates: count %d / %v, want 2", n, err)
	}
	// A rename moves the nickname claim but must not change the player count.
	if _, err := s.Rename(ctx, cntApp, alpha.ID, "Alpha2"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Count(ctx, cntApp); err != nil || n != 2 {
		t.Fatalf("after rename: count %d / %v, want 2", n, err)
	}
	// Delete releases the registration.
	if err := s.Delete(ctx, cntApp, alpha.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Count(ctx, cntApp); err != nil || n != 1 {
		t.Fatalf("after delete: count %d / %v, want 1", n, err)
	}
	// Counts are scoped per app.
	if n, err := s.Count(ctx, cntApp+"x"); err != nil || n != 0 {
		t.Fatalf("unrelated app: count %d / %v, want 0", n, err)
	}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `docker compose run --rm app go test ./pkg/users/... -count=1`

Expected: FAIL — compile error `s.Count undefined (type Store has no field or method Count)`. A compile failure *is* the red state here: the suite is written against an interface method that doesn't exist yet.

- [ ] **Step 3: Write minimal implementation**

In `pkg/users/store.go`, add to the `Store` interface after `Delete` (keep it inside the interface block):

```go
	// Count returns the number of registered players in the app. An unknown
	// app counts zero — this is not an authorization boundary; callers that
	// need one check ownership before calling.
	Count(ctx context.Context, appID string) (int64, error)
```

In `pkg/users/memstore.go`, append:

```go
func (m *MemStore) Count(_ context.Context, appID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return int64(len(m.users[appID])), nil
}
```

In `pkg/users/redisstore.go`, append:

```go
// Count returns the app's registered-player count. The names hash holds
// exactly one field per player — Create HSETs it, Delete HDELs it, and Rename
// HSETs the same field — so HLEN is exact and O(1) with no new index.
func (s *RedisStore) Count(ctx context.Context, appID string) (int64, error) {
	return s.rdb.HLen(ctx, namesKey(appID)).Result()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `docker compose run --rm app go test ./pkg/users/... -count=1 -v -run 'TestMemStore|TestRedisStore'`

Expected: PASS for both `TestMemStore` and `TestRedisStore`. (`TestRedisStore` skips if Redis is unreachable — if you see `redis not available`, the compose Redis isn't up; that is not a pass.)

- [ ] **Step 5: Commit**

```bash
git add pkg/users/store.go pkg/users/memstore.go pkg/users/redisstore.go pkg/users/users_test.go
git commit -m "users: Store.Count for registered players per app"
```

---

### Task 2: `GET /v1/boards/{board}/count`

**Files:**
- Modify: `pkg/api/server.go:198` (route registration) and after `handleSegments` (~line 543) for the handler
- Modify: `README.md:142` (API table)
- Test: `pkg/api/count_test.go` (create)

**Interfaces:**
- Consumes: `engine.RankingEngine.Count(ctx, b) (int64, error)`; `s.readBoard(w, r) (engine.Board, bool)` — resolves the logical board, writes a 404 on failure, and applies `segment`/`window` query params via `physicalBoard`.
- Produces: `GET /v1/boards/{board}/count` → `200 {"count": 1432}`; `404` unknown board. Task 4 consumes this from the web client.

**Context the implementer needs:** The test harness lives in `pkg/api/server_test.go`. `newHarness(t)` builds a server on a real Redis (skips if unavailable); `h.onboard(t, email)` runs signup→verify→login→create-app and populates `h.appID`/`h.apiKey`/`h.csrf`; `h.call(t, method, path, headers, body)` issues a request through a cookie-jar client; `h.key()` returns the API-key header map; `h.cons.Drain(ctx)` flushes the write-behind consumer so submitted scores are queryable. Submits are **write-behind** — always `Drain` before asserting on a read. Model the test on `pkg/api/segments_test.go`.

- [ ] **Step 1: Write the failing test**

Create `pkg/api/count_test.go`:

```go
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kodeni-am/leaderboard/pkg/engine"
)

// countOf GETs a count endpoint with API-key auth and returns the count field.
func countOf(t *testing.T, h *harness, path string) int64 {
	t.Helper()
	resp, body := h.call(t, http.MethodGet, path, h.key(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, resp.StatusCode, body)
	}
	var out struct {
		Count int64 `json:"count"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	return out.Count
}

func TestBoardCount(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "count@example.com")
	ctx := context.Background()
	t.Cleanup(func() {
		for _, seg := range []string{"all", "region=eu"} {
			_ = h.eng.Reset(ctx, engine.Board{Key: engine.BoardKey{App: h.appID, Board: "high", Segment: seg, Window: "all"}})
		}
	})

	if resp, body := h.call(t, http.MethodPost, "/v1/boards", h.key(), map[string]any{"board": "high"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create board: %d %s", resp.StatusCode, body)
	}

	// A fresh board counts zero.
	if n := countOf(t, h, "/v1/boards/high/count"); n != 0 {
		t.Fatalf("fresh board: %d, want 0", n)
	}

	// alice lands in both "all" and "region=eu"; bob only in "all".
	if resp, body := h.call(t, http.MethodPost, "/v1/boards/high/scores", h.key(), map[string]any{"member": "alice", "score": 100, "segments": []string{"all", "region=eu"}}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("submit alice: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.call(t, http.MethodPost, "/v1/boards/high/scores", h.key(), map[string]any{"member": "bob", "score": 50}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("submit bob: %d %s", resp.StatusCode, body)
	}
	if err := h.cons.Drain(ctx); err != nil {
		t.Fatal(err)
	}

	if n := countOf(t, h, "/v1/boards/high/count"); n != 2 {
		t.Fatalf("unfiltered: %d, want 2", n)
	}
	// The segment filter counts only that slice.
	if n := countOf(t, h, "/v1/boards/high/count?segment=region=eu"); n != 1 {
		t.Fatalf("segment=region=eu: %d, want 1", n)
	}
	// A window nothing was submitted to is empty, not an error.
	if n := countOf(t, h, "/v1/boards/high/count?window=daily"); n != 0 {
		t.Fatalf("window=daily: %d, want 0", n)
	}
	// Unknown board -> 404, like the other board reads.
	if resp, _ := h.call(t, http.MethodGet, "/v1/boards/nope/count", h.key(), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown board: %d", resp.StatusCode)
	}
	// Data plane requires auth.
	if resp, _ := h.call(t, http.MethodGet, "/v1/boards/high/count", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `docker compose run --rm app go test ./pkg/api/... -count=1 -run TestBoardCount -v`

Expected: FAIL — the route doesn't exist, so `/v1/boards/high/count` falls through and `countOf` reports a non-200 (`404 {"error":"not found"}`).

- [ ] **Step 3: Write minimal implementation**

In `pkg/api/server.go`, register the route immediately after the `/segments` line (line 198):

```go
	dataPlane("GET /v1/boards/{board}/count", s.handleCount)
```

Add the handler after `handleSegments` (after line 543):

```go
// handleCount returns how many members are on the board as filtered by the
// window/segment params — the same view /top ranks. It is a separate endpoint
// rather than a field on /top so the hot read path pays no ZCARD.
func (s *Server) handleCount(w http.ResponseWriter, r *http.Request) {
	b, ok := s.readBoard(w, r)
	if !ok {
		return
	}
	n, err := s.eng.Count(r.Context(), b)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": n})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `docker compose run --rm app go test ./pkg/api/... -count=1 -run TestBoardCount -v`

Expected: PASS.

- [ ] **Step 5: Document the endpoint**

In `README.md`, add a row to the API table immediately after the `/segments` row (line 142):

```markdown
| `GET /v1/boards/{board}/count` | How many members are on the board (honors `segment=`/`window=`) |
```

- [ ] **Step 6: Commit**

```bash
git add pkg/api/server.go pkg/api/count_test.go README.md
git commit -m "api: GET /v1/boards/{board}/count for board depth"
```

---

### Task 3: `GET /v1/apps/{id}/stats`

**Files:**
- Modify: `pkg/api/server.go:187` (route registration, beside the other `/v1/apps/{id}/...` routes)
- Modify: `pkg/api/auth.go` (handler, after `handleGetSigning` ~line 387)
- Modify: `README.md:133` (API table)
- Test: `pkg/api/count_test.go` (append)

**Interfaces:**
- Consumes: `users.Store.Count(ctx, appID) (int64, error)` from Task 1, reachable as `s.users`; `s.ownedApp(w, r) (tenancy.App, bool)` (`pkg/api/auth.go:309`) — reads `{id}` from the path, verifies the session user owns the app, and writes the `404 app not found` itself.
- Produces: `GET /v1/apps/{id}/stats` → `200 {"players": 5204}`; `401` no session; `404` unknown/unowned app. Task 5 consumes this from the web client.

**Context the implementer needs:** This is the **owner plane** (`user(...)` / `requireUser`), not the data plane — an API key alone must not reach it. Owner-plane GETs need no CSRF header (CSRF is checked only on mutations), and the harness cookie jar carries the session, so `h.call(t, http.MethodGet, path, nil, nil)` is authenticated while a bare `http.Get(h.ts.URL+path)` is not. Note `DELETE /v1/users/{id}` returns **204 No Content** (not 200), and as a session-authed mutation it needs `h.sess()` (which carries `X-App-Id` + `X-CSRF-Token`).

Why not `/v1/users/count`: it would collide with `GET /v1/users/{id}`, and Go's `ServeMux` gives the literal segment precedence — so a player who claimed the member id `count` (which `normalizeMemberID` permits) would silently become unreachable. Do not "simplify" this path back.

- [ ] **Step 1: Write the failing test**

Append to `pkg/api/count_test.go`:

```go
func TestAppStats(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "stats@example.com")

	// Owner-plane GET: the cookie jar carries the session, no CSRF needed.
	players := func() int64 {
		t.Helper()
		resp, body := h.call(t, http.MethodGet, "/v1/apps/"+h.appID+"/stats", nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("stats: %d %s", resp.StatusCode, body)
		}
		var out struct {
			Players int64 `json:"players"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("unmarshal %s: %v", body, err)
		}
		return out.Players
	}

	if n := players(); n != 0 {
		t.Fatalf("new app: %d players, want 0", n)
	}

	resp, body := h.call(t, http.MethodPost, "/v1/users", h.key(), map[string]string{"nickname": "Ninja"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register Ninja: %d %s", resp.StatusCode, body)
	}
	var u struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if resp, body := h.call(t, http.MethodPost, "/v1/users", h.key(), map[string]string{"nickname": "Rook"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("register Rook: %d %s", resp.StatusCode, body)
	}
	if n := players(); n != 2 {
		t.Fatalf("after 2 registrations: %d, want 2", n)
	}

	// Deleting a player drops the count.
	if resp, body := h.call(t, http.MethodDelete, "/v1/users/"+u.UserID, h.sess(), nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete player: %d %s", resp.StatusCode, body)
	}
	if n := players(); n != 1 {
		t.Fatalf("after delete: %d, want 1", n)
	}

	// Owner plane: no session -> 401, even though the app exists.
	r, err := http.Get(h.ts.URL + "/v1/apps/" + h.appID + "/stats")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d, want 401", r.StatusCode)
	}

	// Unknown app -> 404, matching the other owner-plane app routes.
	if resp, _ := h.call(t, http.MethodGet, "/v1/apps/app_nope/stats", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown app: %d, want 404", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `docker compose run --rm app go test ./pkg/api/... -count=1 -run TestAppStats -v`

Expected: FAIL — the route doesn't exist, so `players()` reports a non-200 (`404 {"error":"not found"}`).

- [ ] **Step 3: Write minimal implementation**

In `pkg/api/server.go`, register the route after the signing routes (after line 187):

```go
	user("GET /v1/apps/{id}/stats", s.handleAppStats)
```

In `pkg/api/auth.go`, add the handler after `handleGetSigning` (after line 387):

```go
// handleAppStats returns app-level counters for the dashboard. Owner-plane on
// purpose: playerbase size is an operator metric, and this is not a path we
// want to hand to every API-key holder in one call.
func (s *Server) handleAppStats(w http.ResponseWriter, r *http.Request) {
	app, ok := s.ownedApp(w, r)
	if !ok {
		return
	}
	n, err := s.users.Count(r.Context(), app.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"players": n})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `docker compose run --rm app go test ./pkg/api/... -count=1 -run 'TestAppStats|TestBoardCount' -v`

Expected: PASS for both.

- [ ] **Step 5: Document the endpoint**

In `README.md`, add a row to the API table immediately after the `POST /v1/apps · GET /v1/apps` row (line 133):

```markdown
| `GET /v1/apps/{id}/stats` | App counters — `{"players": N}` registered (session-authed, owner-scoped) |
```

- [ ] **Step 6: Run the full suite and commit**

Run: `make test`
Expected: PASS across all packages (no regressions from the `Store` interface change).

```bash
git add pkg/api/server.go pkg/api/auth.go pkg/api/count_test.go README.md
git commit -m "api: GET /v1/apps/{id}/stats for registered-player count"
```

---

### Task 4: Dashboard — board depth in the viewer header

**Files:**
- Modify: `web/src/api.ts` (add `qsFirst` helper after `qs` at line 84-91; add `count` to the `api` object after `top` at line 127)
- Modify: `web/src/pages/Dashboard.tsx` (the `Viewer` component, lines 494-688)

**Interfaces:**
- Consumes: `GET /v1/boards/{board}/count` → `{count: number}` from Task 2.
- Produces: `api.count(appId, board, q?) => Promise<{count: number}>`.

**Context the implementer needs:** The existing `qs(q)` helper returns a **`&`-prefixed** suffix because its callers already have a first param (`top?n=25`). `/count` has no first param, so it needs a `?`-prefixed suffix — hence `qsFirst`, built on `qs` rather than duplicating it. `Viewer.loadTop` (line 548) is the single refresh path: it runs on mount, on Refresh, on Enter in the window/segment inputs, and after a test submit. Putting the count fetch inside it means the two numbers can never disagree about which view they describe.

- [ ] **Step 1: Add the client method**

In `web/src/api.ts`, add after `qs` (after line 91):

```ts
// qsFirst is qs for endpoints with no preceding param: "?a=b" not "&a=b".
function qsFirst(q?: QueryOpts): string {
  const s = qs(q);
  return s ? "?" + s.slice(1) : "";
}
```

In the `api` object, add after `top` (after line 127):

```ts
  count: (appId: string, board: string, q?: QueryOpts) =>
    req<{ count: number }>("GET", `/v1/boards/${encodeURIComponent(board)}/count${qsFirst(q)}`, undefined, appHdr(appId)),
```

- [ ] **Step 2: Fetch the count alongside the top**

In `web/src/pages/Dashboard.tsx`, add the state beside `entries` in `Viewer` (line 496):

```tsx
  const [count, setCount] = useState<number | null>(null);
```

Replace `loadTop` (lines 548-556) with:

```tsx
  async function loadTop() {
    try {
      // One fetch path for both numbers, so the count always describes the
      // window/segment the entries came from. The count is auxiliary — if it
      // fails, the board still renders (null just hides the "OF N").
      const q = { window: win, segment: seg || undefined };
      const [top, c] = await Promise.all([
        api.top(appId, board, 25, q),
        api.count(appId, board, q).catch(() => null),
      ]);
      setEntries(top.entries);
      setCount(c === null ? null : c.count);
      setErr("");
    } catch (e) {
      setErr((e as ApiError).message);
    }
  }
```

- [ ] **Step 3: Render it in the header**

Replace the panel header (lines 626-628) with:

```tsx
        <div className="eyebrow" style={{ padding: "14px 18px", borderBottom: "1px solid var(--line)" }}>
          TOP {entries.length}{count !== null && count > entries.length ? ` OF ${count.toLocaleString()}` : ""}{win !== "all" ? ` · ${opts.find((o) => o.value === win)?.label ?? win}` : ""}{seg ? ` · ${seg}` : ""}
        </div>
```

The `count > entries.length` guard is deliberate: it avoids the nonsense `TOP 3 OF 3` on small boards, and it degrades to today's `TOP N` when the count is unavailable.

- [ ] **Step 4: Verify the build**

Run: `npm --prefix web run build`
Expected: clean build, no TypeScript errors.

- [ ] **Step 5: Verify in the running app**

Run: `make run`, open the dashboard, register a player and submit scores for more than 25 members on one board (or reuse an existing board).
Expected: the header reads `TOP 25 OF <total>`. Switching the segment filter to one holding fewer members and pressing Enter changes both the rows and the total together.

- [ ] **Step 6: Commit**

```bash
git add web/src/api.ts web/src/pages/Dashboard.tsx
git commit -m "dashboard: show board depth as TOP N OF total"
```

---

### Task 5: Dashboard — registered players beside the app selector

**Files:**
- Modify: `web/src/api.ts` (add `appStats` to the `api` object after `rotateSigning` at line 118)
- Modify: `web/src/pages/Dashboard.tsx` (`Dashboard` lines 7-94, `AppWorkspace` 143-180, `Viewer` 494-688, `TestSubmit` 690-741)

**Interfaces:**
- Consumes: `GET /v1/apps/{id}/stats` → `{players: number}` from Task 3.
- Produces: `api.appStats(appId) => Promise<{players: number}>`. Prop `onPlayersChanged?: () => void` threaded `Dashboard → AppWorkspace → Viewer → TestSubmit`.

**Context the implementer needs:** `Dashboard` owns this state because that's where the app selector lives. The registry is mutated in two places below it — `TestSubmit`'s Register form (line 699) and the Delete player confirmation (`askDeletePlayer`, line 535, which also serves `RankSearch`'s delete via the `onDeletePlayer` prop). Both must refresh the count; a stale number on a surface whose purpose is testing registration reads as broken. Three prop hops is the accepted cost — do not reach for a context or store for one number.

- [ ] **Step 1: Add the client method**

In `web/src/api.ts`, add to the `api` object after `rotateSigning` (after line 118):

```ts
  appStats: (appId: string) => req<{ players: number }>("GET", `/v1/apps/${appId}/stats`),
```

- [ ] **Step 2: Own the count in Dashboard**

In `web/src/pages/Dashboard.tsx`, update the React import (line 1) to include `useCallback`:

```tsx
import { type FormEvent, useCallback, useEffect, useState } from "react";
```

Add state in `Dashboard` after the `err` state (line 15):

```tsx
  const [playerCount, setPlayerCount] = useState<number | null>(null);
```

Add after `loadApps` (after line 31):

```tsx
  // Registered players for the selected app. Auxiliary, like nickname
  // enrichment: a failure hides the number rather than surfacing an error.
  const loadPlayerCount = useCallback(async (id: string) => {
    if (!id) {
      setPlayerCount(null);
      return;
    }
    try {
      const { players } = await api.appStats(id);
      setPlayerCount(players);
    } catch {
      setPlayerCount(null);
    }
  }, []);

  useEffect(() => {
    setPlayerCount(null); // never show the previous app's count against this one
    void loadPlayerCount(appId);
  }, [appId, loadPlayerCount]);
```

- [ ] **Step 3: Render it beside the app selector**

Replace the app-selector row (lines 78-85) with:

```tsx
              <div className="row" style={{ gap: 10 }}>
                <span className="eyebrow">APP</span>
                <select value={appId} onChange={(e) => setAppId(e.target.value)} style={{ width: "auto", minWidth: 220 }}>
                  {apps.map((a) => (
                    <option key={a.id} value={a.id}>{a.name} — {a.id}</option>
                  ))}
                </select>
                {playerCount !== null && (
                  <span className="dim mono" style={{ fontSize: 13 }}>{playerCount.toLocaleString()} players</span>
                )}
              </div>
```

- [ ] **Step 4: Thread the refresh callback down**

In `Dashboard`, pass it to `AppWorkspace` (line 88):

```tsx
            {appId && <AppWorkspace appId={appId} onAppDeleted={() => { setNewKey(""); void loadApps(); }} onPlayersChanged={() => void loadPlayerCount(appId)} />}
```

In `AppWorkspace`, widen the signature (line 143):

```tsx
function AppWorkspace({ appId, onAppDeleted, onPlayersChanged }: { appId: string; onAppDeleted: () => void; onPlayersChanged: () => void }) {
```

and pass it to `Viewer` (line 174):

```tsx
        {board ? <Viewer key={board} appId={appId} board={board} windows={boards.find((b) => b.board === board)?.windows ?? []} onPlayersChanged={onPlayersChanged} /> : (
```

In `Viewer`, widen the signature (line 494):

```tsx
function Viewer({ appId, board, windows, onPlayersChanged }: { appId: string; board: string; windows: WindowSpec[]; onPlayersChanged: () => void }) {
```

- [ ] **Step 5: Refresh on the two registry mutations**

In `Viewer.askDeletePlayer`, update `onYes` (lines 541-545) so a deleted player drops the count:

```tsx
      onYes: async () => {
        await api.deleteUser(appId, member);
        onPlayersChanged();
        await loadTop();
      },
```

Pass the callback to `TestSubmit` (line 602-606) by adding the prop:

```tsx
      <TestSubmit
        appId={appId}
        board={board}
        segment={seg}
        busy={busy}
        onRegistered={onPlayersChanged}
```

In `TestSubmit`, widen the signature (line 690):

```tsx
function TestSubmit({ appId, board, segment, busy, onRegistered, onSubmit }: { appId: string; board: string; segment: string; busy: boolean; onRegistered: () => void; onSubmit: (m: string, s: number) => void }) {
```

and call it after a successful register, inside `register` (after line 704, following `setRegMsg`):

```tsx
      onRegistered();
```

- [ ] **Step 6: Verify the build**

Run: `npm --prefix web run build`
Expected: clean build, no TypeScript errors.

- [ ] **Step 7: Verify in the running app**

Run: `make run` and open the dashboard.
Expected: `N players` shows beside the app selector. Registering a player through the Register form increments it without a page reload; Delete player decrements it. Switching apps shows the new app's number (never briefly the old one). An app with no registered players shows nothing rather than `0 players`.

- [ ] **Step 8: Commit**

```bash
git add web/src/api.ts web/src/pages/Dashboard.tsx
git commit -m "dashboard: show registered player count beside the app selector"
```

---

## Self-Review

**Spec coverage:** engine (no changes — Task 2 uses existing `Count`) ✓; `users.Store.Count` incl. `HLEN`/`len(map)` ✓ (Task 1); board count endpoint honoring window/segment + 404 ✓ (Task 2); `/v1/apps/{id}/stats` on the owner plane via `ownedApp` + the "why not `/v1/users/count`" rationale ✓ (Task 3); `api.ts` `count`/`appStats` ✓ (Tasks 4/5); Viewer `TOP N OF M` with the `count > entries.length` guard and single `loadTop` fetch path ✓ (Task 4); Dashboard count beside the selector, cleared on app switch, refreshed on register/delete ✓ (Task 5); best-effort error handling ✓ (Tasks 4/5); store tests in their own app namespace ✓ (Task 1); API tests incl. auth/404 cases ✓ (Tasks 2/3); README rows ✓ (Tasks 2/3); SDKs excluded ✓.

**Placeholders:** none — every code step carries complete code and every run step an exact command with expected output.

**Type consistency:** `Count(ctx, appID) (int64, error)` is defined in Task 1 and consumed in Task 3 with matching types. `{"count": N}` (Task 2) is consumed as `{count: number}` (Task 4); `{"players": N}` (Task 3) as `{players: number}` (Task 5). `onPlayersChanged` is named consistently through `Dashboard`/`AppWorkspace`/`Viewer`; `TestSubmit` receives it as `onRegistered` (its local name — passed as `onRegistered={onPlayersChanged}` in Task 5, Step 5). `qsFirst` is defined in Task 4 and used only there.
