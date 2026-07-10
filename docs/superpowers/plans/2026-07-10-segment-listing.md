# Segment Listing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enumerate the segments currently live on a board — engine method, `GET /v1/boards/{board}/segments`, and dashboard datalist suggestions.

**Architecture:** On-demand SCAN (Approach A from the spec): `engine.Segments` reuses the existing glob-escaped `scanBoardKeys` helper to parse segment names out of live `lb:{app:board:segment:window}:z` keys — no write-path cost, no new data structures. The API resolves the board and returns the sorted list; the dashboard's free-text segment filter gains a `<datalist>` fed by it (suggestions only, not validation).

**Tech Stack:** Go (go-redis v9), React + TypeScript (vite).

**Spec:** `docs/superpowers/specs/2026-07-10-segment-listing-design.md`

## Global Constraints

- **Docker-only Go toolchain**: never run `go` on the host. All Go commands run as `docker compose run --rm app go <args>` from the repo root; full suite is `make test`. Web builds run on the host: `npm --prefix web run build`.
- `Segments` returns a **deduplicated, lexically sorted** list **including `"all"`** (the segment unsegmented submits land in). Empty board → empty **non-nil** slice; the API response `segments` field must marshal as `[]`, never `null`.
- Endpoint is on the data plane (`requireApp`); 404 for an unknown board.
- The dashboard list is suggestions only — blank still means "all segments", free typing still works. A fetch failure leaves the datalist empty (best-effort).
- Commit after every task.

---

### Task 1: `engine.Segments` — enumerate live segments by scan

**Files:**
- Modify: `pkg/engine/engine.go` (interface)
- Modify: `pkg/engine/redis_engine.go` (implementation; `sort` is already imported)
- Modify: `pkg/engine/sharded_engine.go` (union across shards; add `"sort"` to its imports)
- Test: `pkg/engine/redis_engine_test.go` (add `"reflect"` to its imports)

**Interfaces:**
- Consumes: `scanBoardKeys(ctx, rdb, app, board) ([]BoardKey, error)` (existing, glob-escaped), `ShardedEngine`'s `re` field / `shards` count / `board + "#s" + i` suffix scheme.
- Produces: `Segments(ctx context.Context, lb LogicalBoard) ([]string, error)` on the `RankingEngine` interface, implemented by both engines. Task 2's handler calls `s.eng.Segments`.

- [ ] **Step 1: Write the failing tests**

Add to `pkg/engine/redis_engine_test.go` (add `"reflect"` to the import block; `context`, `strings`, `time` are already imported):

```go
func TestSegments(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	app := strings.NewReplacer("/", "-", " ", "_").Replace(t.Name())
	lb := LogicalBoard{App: app, Board: "b", Windows: []WindowSpec{{Kind: WindowAllTime}, {Kind: WindowDaily}}}
	now := time.Now().UTC()
	past := now.AddDate(0, 0, -3)

	// Empty board: no live keys yet -> empty, non-nil slice.
	segs, err := e.Segments(ctx, lb)
	if err != nil {
		t.Fatal(err)
	}
	if segs == nil || len(segs) != 0 {
		t.Fatalf("empty board: got %#v, want empty non-nil slice", segs)
	}

	var boards []Board
	// Current-time submit into two segments (fans out to all-time + today's daily).
	for _, k := range DerivePhysicalBoards(lb, Event{Member: "alice", Score: 100, Time: now, Segments: []string{"all", "region=eu"}}) {
		b := Board{Key: k, Config: lb.Config}
		boards = append(boards, b)
		if _, err := e.Submit(ctx, b, "alice", 100, now); err != nil {
			t.Fatal(err)
		}
	}
	// A segment that exists ONLY in a stale past daily window (pre-reaper).
	pastDaily := Board{Key: BoardKey{App: app, Board: "b", Segment: "s=old",
		Window: (WindowSpec{Kind: WindowDaily}).WindowID(past)}, Config: lb.Config}
	boards = append(boards, pastDaily)
	if _, err := e.Submit(ctx, pastDaily, "carol", 10, past); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, b := range boards {
			_ = e.Reset(ctx, b)
		}
	})

	segs, err = e.Segments(ctx, lb)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"all", "region=eu", "s=old"}; !reflect.DeepEqual(segs, want) {
		t.Fatalf("got %v, want %v (deduped across windows, sorted, stale window included)", segs, want)
	}
}

func TestSegmentsEscapesGlobMeta(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	app := strings.NewReplacer("/", "-", " ", "_").Replace(t.Name())
	starB := Board{Key: BoardKey{App: app, Board: "b*", Segment: "star-seg", Window: "all"}}
	otherB := Board{Key: BoardKey{App: app, Board: "bZ", Segment: "other-seg", Window: "all"}}
	now := time.Now().UTC()
	for _, b := range []Board{starB, otherB} {
		if _, err := e.Submit(ctx, b, "alice", 1, now); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = e.Reset(ctx, starB)
		_ = e.Reset(ctx, otherB)
	})

	segs, err := e.Segments(ctx, LogicalBoard{App: app, Board: "b*"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(segs, []string{"star-seg"}) {
		t.Fatalf("glob leak: board b* listed %v", segs)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `docker compose run --rm app go test ./pkg/engine/... -count=1 -run TestSegments`
Expected: FAIL to compile — `e.Segments undefined`

- [ ] **Step 3: Implement**

`pkg/engine/engine.go` — add to the `RankingEngine` interface after `RemoveFromAll`:

```go
	// Segments returns the deduplicated, lexically sorted segment names that
	// currently have live physical boards for lb — including "all", where
	// unsegmented submits land. An empty board yields an empty non-nil slice.
	Segments(ctx context.Context, lb LogicalBoard) ([]string, error)
```

`pkg/engine/redis_engine.go` — add after `RemoveFromAll`:

```go
// Segments returns the deduplicated, sorted segment names with live physical
// boards for lb. Like RemoveFromAll, it lists what the scan can see: segments
// whose only keys lived in dated windows disappear once the reaper expires
// them (boards with an all-time window retain every segment ever used).
func (e *RedisEngine) Segments(ctx context.Context, lb LogicalBoard) ([]string, error) {
	keys, err := scanBoardKeys(ctx, e.rdb, lb.App, lb.Board)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(keys))
	segs := make([]string, 0, len(keys))
	for _, k := range keys {
		if !seen[k.Segment] {
			seen[k.Segment] = true
			segs = append(segs, k.Segment)
		}
	}
	sort.Strings(segs)
	return segs, nil
}
```

`pkg/engine/sharded_engine.go` — add `"sort"` to the imports, then add after `RemoveFromAll`:

```go
// Segments unions the live segment names across all shards of lb (each shard
// holds its own physical keys under the board#s<i> name).
func (s *ShardedEngine) Segments(ctx context.Context, lb LogicalBoard) ([]string, error) {
	seen := map[string]bool{}
	segs := []string{}
	for i := 0; i < s.shards; i++ {
		sharded := lb
		sharded.Board = lb.Board + "#s" + strconv.Itoa(i)
		part, err := s.re.Segments(ctx, sharded)
		if err != nil {
			return nil, err
		}
		for _, sg := range part {
			if !seen[sg] {
				seen[sg] = true
				segs = append(segs, sg)
			}
		}
	}
	sort.Strings(segs)
	return segs, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `docker compose run --rm app go test ./pkg/engine/... -count=1`
Expected: PASS (all engine tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/engine
git commit -m "engine: Segments lists live segment names for a logical board"
```

---

### Task 2: API — `GET /v1/boards/{board}/segments`

**Files:**
- Modify: `pkg/api/server.go` (handler + route)
- Test: Create `pkg/api/segments_test.go`

**Interfaces:**
- Consumes: `s.eng.Segments(ctx, lb)` (Task 1), `s.resolveBoard`, `requireApp` data plane, test harness (`newHarness`/`onboard`/`call`/`key()` in `server_test.go`).
- Produces: `GET /v1/boards/{board}/segments` → `200 {"segments":[...]}` (always a JSON array), `404` unknown board. Task 3's `api.segments` calls it.

- [ ] **Step 1: Write the failing test**

Create `pkg/api/segments_test.go`:

```go
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/kodeni-am/leaderboard/pkg/engine"
)

func TestListSegments(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "segs@example.com")
	ctx := context.Background()
	t.Cleanup(func() {
		for _, seg := range []string{"all", "region=eu"} {
			_ = h.eng.Reset(ctx, engine.Board{Key: engine.BoardKey{App: h.appID, Board: "high", Segment: seg, Window: "all"}})
		}
	})

	if resp, body := h.call(t, http.MethodPost, "/v1/boards", h.key(), map[string]any{"board": "high"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create board: %d %s", resp.StatusCode, body)
	}

	// Fresh board: an empty JSON array, not null.
	resp, body := h.call(t, http.MethodGet, "/v1/boards/high/segments", h.key(), nil)
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != `{"segments":[]}` {
		t.Fatalf("fresh board: %d %s", resp.StatusCode, body)
	}

	if resp, body := h.call(t, http.MethodPost, "/v1/boards/high/scores", h.key(), map[string]any{"member": "alice", "score": 100, "segments": []string{"all", "region=eu"}}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("submit: %d %s", resp.StatusCode, body)
	}
	if err := h.cons.Drain(ctx); err != nil {
		t.Fatal(err)
	}

	resp, body = h.call(t, http.MethodGet, "/v1/boards/high/segments", h.key(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("segments: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Segments []string `json:"segments"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if !reflect.DeepEqual(out.Segments, []string{"all", "region=eu"}) {
		t.Fatalf("got %v, want [all region=eu]", out.Segments)
	}

	if resp, _ := h.call(t, http.MethodGet, "/v1/boards/nope/segments", h.key(), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown board: %d", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `docker compose run --rm app go test ./pkg/api/... -count=1 -run TestListSegments`
Expected: FAIL — `fresh board: 404 ...` (route doesn't exist yet)

- [ ] **Step 3: Implement**

`pkg/api/server.go` — in `Handler()`, after the `dataPlane("GET /v1/boards/{board}/neighbors", s.handleNeighbors)` line, add:

```go
	dataPlane("GET /v1/boards/{board}/segments", s.handleSegments)
```

And add the handler in the `--- queries ---` section (after `handleNeighbors`):

```go
// handleSegments lists the segment names currently live for the board —
// suggestions for the dashboard's segment filter and for API discovery.
// Segments are ad-hoc (declared per submit, not on the board), so the list
// reflects what the cache holds now, not an authoritative registry.
func (s *Server) handleSegments(w http.ResponseWriter, r *http.Request) {
	app, _ := tenancy.AppFromContext(r.Context())
	lb, err := s.resolveBoard(r.Context(), app.ID, r.PathValue("board"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "unknown board")
		return
	}
	segs, err := s.eng.Segments(r.Context(), lb)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if segs == nil { // defensive: the field must marshal as [], never null
		segs = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"segments": segs})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `docker compose run --rm app go test ./pkg/api/... -count=1`
Expected: PASS

- [ ] **Step 5: Run the full suite and commit**

Run: `make test`
Expected: all packages `ok`

```bash
git add pkg/api
git commit -m "api: GET /v1/boards/{board}/segments lists live segments"
```

---

### Task 3: Dashboard datalist + spec status

**Files:**
- Modify: `web/src/api.ts` (one client function)
- Modify: `web/src/pages/Dashboard.tsx` (`Viewer`: state, fetch, datalist)
- Modify: `docs/superpowers/specs/2026-07-10-segment-listing-design.md` (status)

**Interfaces:**
- Consumes: Task 2's endpoint; existing `req`/`appHdr` helpers; `Viewer`'s existing `seg` state and board-change `useEffect` (`Dashboard.tsx:555-558`).
- Produces: `api.segments(appId, board)`; segment input suggestions.

- [ ] **Step 1: Add the API client function**

In `web/src/api.ts`, add to the `api` object after `neighbors` (before `removeScore`):

```ts
  segments: (appId: string, board: string) =>
    req<{ segments: string[] }>("GET", `/v1/boards/${encodeURIComponent(board)}/segments`, undefined, appHdr(appId)),
```

- [ ] **Step 2: Wire the Viewer datalist**

In `web/src/pages/Dashboard.tsx`, inside `Viewer`:

a) Next to the existing `const [seg, setSeg] = useState("");` add:

```tsx
  // Live segment names for the datalist — suggestions only (segments are
  // ad-hoc per submit; blank still means "all segments", free typing works).
  const [segOpts, setSegOpts] = useState<string[]>([]);
```

b) Extend the existing board-change effect (currently `useEffect(() => { void loadTop(); ... }, [appId, board])`) to also fetch suggestions, best-effort:

```tsx
  useEffect(() => {
    void loadTop();
    api.segments(appId, board).then((r) => setSegOpts(r.segments)).catch(() => setSegOpts([]));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [appId, board]);
```

c) Give the segment input a `list` attribute and add the datalist right after it (mirroring the window input/datalist pair above it):

```tsx
          <input
            list={`seg-${board}`}
            value={seg}
            onChange={(e) => setSeg(e.target.value)}
            onKeyDown={(e) => { if (e.key === "Enter") void loadTop(); }}
            placeholder="all segments"
            title="Segment filter, e.g. region=eu (blank = all)"
            style={{ width: 130 }}
          />
          <datalist id={`seg-${board}`}>
            {segOpts.map((s) => <option key={s} value={s} />)}
          </datalist>
```

- [ ] **Step 3: Build**

Run: `npm --prefix web run build`
Expected: `tsc --noEmit` clean, vite build succeeds

- [ ] **Step 4: Verify in the running app**

Run `docker compose up --build -d leaderboardd`; in the dashboard, pick a board, submit scores with a segment (e.g. `region=eu`), refresh, then focus the segment filter — the datalist should suggest `all` and `region=eu`. Then `docker compose stop`.

- [ ] **Step 5: Mark the spec implemented and commit**

In `docs/superpowers/specs/2026-07-10-segment-listing-design.md` change `**Status:** Approved for implementation` to `**Status:** Implemented`.

```bash
git add web/src docs/superpowers/specs/2026-07-10-segment-listing-design.md
git commit -m "dashboard: segment filter suggestions from live segment list"
```
