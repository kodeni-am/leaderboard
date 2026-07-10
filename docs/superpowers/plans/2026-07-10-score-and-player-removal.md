# Score & Player Removal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebuild-safe removal of a member's entry from a board, and full player deletion (all boards + registry), exposed via API, dashboard, and all three SDKs.

**Architecture:** Removals are durable tombstone records (`op: "remove"`) in the ingest log — the commit point — applied immediately by the API handler for read-your-writes and replayed by consumers/rebuild. "Delete player" decomposes into one per-board tombstone (same log partition as that board's submits, so replay order holds) plus a synchronous registry deletion (the registry is primary data, never rebuilt from the log). The engine gains `RemoveFromAll`, which SCANs a logical board's live physical keys (all segments/windows) and removes the member from each.

**Tech Stack:** Go (stdlib net/http, go-redis v9, Redis Lua), React + TypeScript (vite), Unity C#.

**Spec:** `docs/superpowers/specs/2026-07-10-score-and-player-removal-design.md`

## Global Constraints

- **Docker-only toolchain**: never run `go` on the host. All Go commands run as
  `docker compose run --rm app go <args>` (Redis is available inside at the
  compose default; tests read `REDIS_ADDR` or default `localhost:6379`, which
  works inside the `app` container network). `make test` runs the whole suite.
- Web/SDK JS builds run on the host with npm (`web/node_modules` exists).
- Existing log records have no `op` field and MUST keep decoding as submits.
- New endpoints are data-plane (`requireApp`): API key OR session + `X-App-Id`
  (+ CSRF on mutations) both work.
- Removal is idempotent: 204 even when the member had no entry/registration.
- Error code string for append-succeeded-but-apply-failed: `removal_queued`.
- Commit after every task with a short imperative message.

---

### Task 1: `users.Delete` — registry deletion with nickname-ownership guard

**Files:**
- Modify: `pkg/users/store.go` (interface + error var)
- Modify: `pkg/users/memstore.go`
- Modify: `pkg/users/redisstore.go`
- Test: `pkg/users/users_test.go` (extend the `testStore` conformance suite)

**Interfaces:**
- Consumes: existing `users.Store`, `normalizeNickname`, key helpers.
- Produces: `Delete(ctx context.Context, appID, id string) error` on the
  `users.Store` interface (nil on unknown id), and
  `users.ErrDeleteContention`. Task 5's API handler calls `s.users.Delete`.

- [ ] **Step 1: Write the failing test**

Append to the `testStore` conformance suite in `pkg/users/users_test.go` (at the end of the function, after the existing Nicknames assertions — it runs against both MemStore and RedisStore already):

```go
	// Delete removes the registration and releases the nickname for re-claim.
	del, err := s.Create(ctx, app, "Vanish")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, app, del.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, app, del.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete: %v, want ErrNotFound", err)
	}
	if n, err := s.Nicknames(ctx, app, []string{del.ID}); err != nil || len(n) != 0 {
		t.Errorf("Nicknames after delete: %v / %v", n, err)
	}
	reclaimed, err := s.Create(ctx, app, "Vanish")
	if err != nil {
		t.Fatalf("nickname not released: %v", err)
	}

	// Deleting an unknown id is a no-op (idempotent).
	if err := s.Delete(ctx, app, "plr_nope"); err != nil {
		t.Errorf("Delete unknown: %v, want nil", err)
	}

	// Replayed delete of the OLD id must not touch the re-claimed nickname:
	// the claim now maps to reclaimed.ID, not del.ID.
	if err := s.Delete(ctx, app, del.ID); err != nil {
		t.Errorf("replayed delete: %v", err)
	}
	if got, err := s.GetByNickname(ctx, app, "vanish"); err != nil || got.ID != reclaimed.ID {
		t.Errorf("re-claimed nickname lost after replayed delete: %+v / %v", got, err)
	}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `docker compose run --rm app go test ./pkg/users/... -count=1`
Expected: FAIL to compile — `s.Delete undefined (type Store has no field or method Delete)`

- [ ] **Step 3: Implement**

`pkg/users/store.go` — add to the error var block:

```go
	// ErrDeleteContention is returned when a delete kept losing to concurrent
	// renames of the same player and exhausted its retries.
	ErrDeleteContention = errors.New("users: delete contention, retry")
```

Add to the `Store` interface after `Rename`:

```go
	// Delete removes a player's registration and releases the nickname for
	// re-use. Unknown ids are a no-op (nil) so deletion is idempotent. The
	// nickname claim is released only if it still maps to this id — player ids
	// are never reused, so a replayed delete can never affect a later
	// registration that re-claimed the name.
	Delete(ctx context.Context, appID, id string) error
```

`pkg/users/memstore.go` — add:

```go
func (m *MemStore) Delete(_ context.Context, appID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[appID][id]
	if !ok {
		return nil
	}
	_, lower, err := normalizeNickname(u.Nickname)
	if err == nil && m.nicks[appID][lower] == id {
		delete(m.nicks[appID], lower)
	}
	delete(m.users[appID], id)
	return nil
}
```

`pkg/users/redisstore.go` — add `"errors"` to the imports, then:

```go
// deleteScript removes the player record and id->display mapping, releasing
// the lowercased nickname claim only if it still maps to this player.
// Returns -1 when the caller's nickname snapshot is stale (a concurrent
// rename moved it) so the caller re-reads and retries.
// KEYS: 1=nick hash, 2=names hash, 3=user record
// ARGV: 1=lower nick, 2=id
var deleteScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[3]) == 0 then return 1 end
if redis.call('HGET', KEYS[1], ARGV[1]) ~= ARGV[2] then return -1 end
redis.call('HDEL', KEYS[1], ARGV[1])
redis.call('HDEL', KEYS[2], ARGV[2])
redis.call('DEL', KEYS[3])
return 1
`)

func (s *RedisStore) Delete(ctx context.Context, appID, id string) error {
	// Same stale-snapshot retry pattern as Rename: a concurrent rename of the
	// same player invalidates our lowercased-nickname snapshot (-1).
	const maxAttempts = 32
	for attempt := 0; attempt < maxAttempts; attempt++ {
		u, err := s.Get(ctx, appID, id)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		_, lower, err := normalizeNickname(u.Nickname)
		if err != nil {
			return err
		}
		res, err := deleteScript.Run(ctx, s.rdb,
			[]string{nickKey(appID), namesKey(appID), playerKey(appID, id)},
			lower, id).Int()
		if err != nil {
			return err
		}
		if res == 1 {
			return nil
		}
		// res == -1: stale snapshot, retry.
	}
	return ErrDeleteContention
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `docker compose run --rm app go test ./pkg/users/... -count=1`
Expected: PASS (both MemStore and RedisStore conformance runs)

- [ ] **Step 5: Commit**

```bash
git add pkg/users
git commit -m "users: Delete releases registration and nickname with ownership guard"
```

---

### Task 2: `engine.RemoveFromAll` — remove a member from every live physical board

**Files:**
- Modify: `pkg/engine/engine.go` (interface)
- Modify: `pkg/engine/redis_engine.go` (scan helper + implementation)
- Modify: `pkg/engine/sharded_engine.go` (member-shard delegation)
- Test: `pkg/engine/redis_engine_test.go`

**Interfaces:**
- Consumes: `RedisEngine.Remove` (`redis_engine.go:392`, already maintains
  sorted set + approx histogram), `BoardKey` component validation (no `:`
  allowed — makes hash-tag parsing unambiguous), `ShardedEngine.shardOf` /
  shard suffix scheme `board + "#s" + i` (`sharded_engine.go:60-62`).
- Produces: `RemoveFromAll(ctx context.Context, lb LogicalBoard, member string) error`
  on the `RankingEngine` interface, implemented by both engines. Used by
  Tasks 4 (consumers) and 5 (API handlers).

- [ ] **Step 1: Write the failing test**

Add to `pkg/engine/redis_engine_test.go` (uses the file's existing `testEngine` and `members` helpers):

```go
func TestRemoveFromAll(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	app := strings.NewReplacer("/", "-", " ", "_").Replace(t.Name())
	lb := LogicalBoard{App: app, Board: "b", Windows: []WindowSpec{{Kind: WindowAllTime}, {Kind: WindowDaily}}}
	now := time.Now().UTC()

	// Write alice+bob into every window/segment combo a submit would touch,
	// on two segments, plus a PAST daily window (stale but still live in the
	// cache — exactly what the reaper hasn't swept yet).
	past := now.AddDate(0, 0, -3)
	var boards []Board
	for _, ev := range []Event{
		{Member: "alice", Score: 100, Time: now, Segments: []string{"all", "region=eu"}},
		{Member: "alice", Score: 90, Time: past, Segments: []string{"all"}},
		{Member: "bob", Score: 50, Time: now, Segments: []string{"all", "region=eu"}},
	} {
		for _, k := range DerivePhysicalBoards(lb, ev) {
			b := Board{Key: k, Config: lb.Config}
			boards = append(boards, b)
			if _, err := e.Submit(ctx, b, ev.Member, ev.Score, ev.Time); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(func() {
		for _, b := range boards {
			_ = e.Reset(ctx, b)
		}
	})

	if err := e.RemoveFromAll(ctx, lb, "alice"); err != nil {
		t.Fatal(err)
	}
	for _, b := range boards {
		if _, err := e.GetRank(ctx, b, "alice"); !errors.Is(err, ErrMemberNotFound) {
			t.Errorf("alice still on %s: %v", b.Key, err)
		}
	}
	// bob is untouched on the current-window boards.
	cur := Board{Key: BoardKey{App: app, Board: "b", Segment: "all", Window: "all"}, Config: lb.Config}
	if re, err := e.GetRank(ctx, cur, "bob"); err != nil || re.Rank != 1 {
		t.Errorf("bob: %+v / %v", re, err)
	}
	// Removing an absent member is a no-op.
	if err := e.RemoveFromAll(ctx, lb, "ghost"); err != nil {
		t.Errorf("remove absent: %v", err)
	}
}

func TestRemoveFromAllMaintainsHistogram(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	cfg := BoardConfig{ApproxRank: true, ApproxMin: 0, ApproxMax: 1000, ApproxBuckets: 16}
	b := freshBoard(t, e, cfg)
	lb := LogicalBoard{App: b.Key.App, Board: b.Key.Board, Config: cfg}
	now := time.Now().UTC()
	for m, sc := range map[string]float64{"alice": 900, "bob": 500, "carol": 100} {
		if _, err := e.Submit(ctx, b, m, sc, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.RemoveFromAll(ctx, lb, "alice"); err != nil {
		t.Fatal(err)
	}
	// With alice's bucket decremented, bob's approximate rank is 1 again.
	re, err := e.GetApproxRank(ctx, b, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if re.Rank != 1 {
		t.Errorf("approx rank after removal: got %d, want 1 (histogram not decremented?)", re.Rank)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `docker compose run --rm app go test ./pkg/engine/... -count=1 -run TestRemoveFromAll`
Expected: FAIL to compile — `e.RemoveFromAll undefined`

- [ ] **Step 3: Implement**

`pkg/engine/engine.go` — add to the `RankingEngine` interface after `Remove`:

```go
	// RemoveFromAll removes member from every live physical board of the
	// logical board — all segments and all windows currently in the cache,
	// including past windows the reaper has not yet expired. Approx-board
	// histograms are maintained. Removing an absent member is a no-op.
	RemoveFromAll(ctx context.Context, lb LogicalBoard, member string) error
```

`pkg/engine/redis_engine.go` — ensure `"strings"` is imported, then add after `Remove`:

```go
// scanBoardKeys returns the BoardKey of every live sorted set belonging to
// (app, board) — one per segment/window combination. Key components cannot
// contain ':' (validated on write), so splitting the hash tag is unambiguous.
// Like the window reaper's sweep, SCAN has single-node scope on Redis Cluster.
func scanBoardKeys(ctx context.Context, rdb redis.UniversalClient, app, board string) ([]BoardKey, error) {
	pattern := "lb:{" + app + ":" + board + ":*}:z"
	var keys []BoardKey
	var cursor uint64
	for {
		batch, next, err := rdb.Scan(ctx, cursor, pattern, 200).Result()
		if err != nil {
			return nil, err
		}
		for _, raw := range batch {
			open := strings.IndexByte(raw, '{')
			close := strings.IndexByte(raw, '}')
			if open < 0 || close < open {
				continue
			}
			parts := strings.Split(raw[open+1:close], ":")
			if len(parts) != 4 {
				continue
			}
			keys = append(keys, BoardKey{App: parts[0], Board: parts[1], Segment: parts[2], Window: parts[3]})
		}
		cursor = next
		if cursor == 0 {
			return keys, nil
		}
	}
}

// RemoveFromAll removes member from every live physical board of lb.
func (e *RedisEngine) RemoveFromAll(ctx context.Context, lb LogicalBoard, member string) error {
	keys, err := scanBoardKeys(ctx, e.rdb, lb.App, lb.Board)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if err := e.Remove(ctx, Board{Key: k, Config: lb.Config}, member); err != nil {
			return err
		}
	}
	return nil
}
```

`pkg/engine/sharded_engine.go` — add after `Remove` (note: writes route by member, so the member can only exist on its own shard; the scan pattern only needs that shard's board name):

```go
func (s *ShardedEngine) RemoveFromAll(ctx context.Context, lb LogicalBoard, member string) error {
	sharded := lb
	sharded.Board = lb.Board + "#s" + strconv.Itoa(s.shardOf(member))
	return s.re.RemoveFromAll(ctx, sharded, member)
}
```

(If the field holding the inner engine is named differently than `re`, match the name used by the existing `Remove` at `sharded_engine.go:80`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `docker compose run --rm app go test ./pkg/engine/... -count=1`
Expected: PASS (all engine tests, including the two new ones)

- [ ] **Step 5: Commit**

```bash
git add pkg/engine
git commit -m "engine: RemoveFromAll removes a member from every live physical board"
```

---

### Task 3: Tombstone records — `Record.Op` and `Ingestor.Remove`

**Files:**
- Modify: `pkg/ingest/log.go` (Op field + constants)
- Modify: `pkg/ingest/ingestor.go` (Remove method)
- Test: `pkg/ingest/ingest_unit_test.go`

**Interfaces:**
- Consumes: `Log.Append`, `BoardResolver.Resolve`, `ErrUnknownBoard`.
- Produces: `Record.Op string` (JSON `op,omitempty`), constants
  `OpSubmit = ""` and `OpRemove = "remove"`, and
  `(*Ingestor).Remove(ctx context.Context, rec Record) error`
  (fills `Op`/`Time`; returns `ErrUnknownBoard` for unregistered boards).
  Used by Task 4 (consumers branch on `Op`) and Task 5 (API appends).

- [ ] **Step 1: Write the failing tests**

Add to `pkg/ingest/ingest_unit_test.go`:

```go
func TestRecordOpBackwardCompatible(t *testing.T) {
	// Pre-existing log entries have no "op" field and must decode as submits.
	var rec Record
	if err := json.Unmarshal([]byte(`{"app":"a","board":"b","member":"m","score":5}`), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Op != OpSubmit {
		t.Fatalf("legacy record decoded with op %q, want OpSubmit", rec.Op)
	}
	// A submit record must not serialize an op field (omitempty).
	data, _ := json.Marshal(Record{App: "a", Board: "b", Member: "m"})
	if strings.Contains(string(data), `"op"`) {
		t.Fatalf("submit record serialized an op field: %s", data)
	}
	// A tombstone round-trips its op.
	data, _ = json.Marshal(Record{App: "a", Board: "b", Member: "m", Op: OpRemove})
	var back Record
	_ = json.Unmarshal(data, &back)
	if back.Op != OpRemove {
		t.Fatalf("tombstone round-trip lost op: %s", data)
	}
}

func TestIngestorRemove(t *testing.T) {
	ctx := context.Background()
	reg := NewStaticRegistry()
	reg.Register(engine.LogicalBoard{App: "app", Board: "score"})
	log := NewMemLog()
	ing := NewIngestor(log, reg, NewMemDeduper())

	if err := ing.Remove(ctx, Record{App: "app", Board: "nope", Member: "m"}); !errors.Is(err, ErrUnknownBoard) {
		t.Fatalf("unknown board: %v", err)
	}
	if err := ing.Remove(ctx, Record{App: "app", Board: "score", Member: "cheater"}); err != nil {
		t.Fatal(err)
	}
	recs, err := log.ReadPartition(ctx, 0, "", 10)
	if err != nil || len(recs) != 1 {
		t.Fatalf("log: %v / %v", recs, err)
	}
	if recs[0].Op != OpRemove || recs[0].Member != "cheater" || recs[0].Time.IsZero() {
		t.Fatalf("bad tombstone: %+v", recs[0])
	}
}
```

Add any missing imports (`encoding/json`, `errors`, `strings`, `github.com/kodeni-am/leaderboard/pkg/engine`) to the test file if not already present.

- [ ] **Step 2: Run tests to verify they fail**

Run: `docker compose run --rm app go test ./pkg/ingest/... -count=1 -run "TestRecordOp|TestIngestorRemove"`
Expected: FAIL to compile — `undefined: OpSubmit`, `ing.Remove undefined`

- [ ] **Step 3: Implement**

`pkg/ingest/log.go` — add above the `Record` type:

```go
// Record op types. The zero value is a score submit, so every pre-existing
// log entry (which has no op field) decodes as a submit.
const (
	OpSubmit = ""       // score submission (default)
	OpRemove = "remove" // tombstone: remove Member's entry from Board everywhere
)
```

Add to the `Record` struct after `Idem`:

```go
	// Op discriminates the record type: OpSubmit ("") or OpRemove. Tombstones
	// use App/Board/Member; Score and Segments are meaningless on them.
	Op string `json:"op,omitempty"`
```

`pkg/ingest/ingestor.go` — add after `Submit`:

```go
// Remove durably appends a removal tombstone for (rec.Board, rec.Member).
// It partitions like the member's submits on that board, so consumers and
// rebuild apply it in order relative to them. Applying the removal to the
// ranking tier happens asynchronously in the consumer; callers wanting
// read-your-writes additionally apply it synchronously (removal is
// idempotent, so double application is harmless).
func (i *Ingestor) Remove(ctx context.Context, rec Record) error {
	if _, ok := i.resolver.Resolve(rec.App, rec.Board); !ok {
		return ErrUnknownBoard
	}
	rec.Op = OpRemove
	if rec.Time.IsZero() {
		rec.Time = time.Now().UTC()
	}
	return i.log.Append(ctx, &rec)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `docker compose run --rm app go test ./pkg/ingest/... -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/ingest
git commit -m "ingest: removal tombstone records (op=remove) and Ingestor.Remove"
```

---

### Task 4: Consumers apply tombstones in order; rebuild reproduces deletions

**Files:**
- Modify: `pkg/ingest/consumer.go` (replace `recordsToOps` with order-aware `applyRecords`)
- Modify: `pkg/ingest/group.go` (route through `applyRecords`)
- Test: `pkg/ingest/consumer_test.go`, `pkg/ingest/group_test.go`

**Interfaces:**
- Consumes: `Record.Op`/`OpRemove` (Task 3), `RankingEngine.RemoveFromAll`
  (Task 2), existing `recordToOps` fan-out.
- Produces: `applyRecords(ctx, eng, resolver, recs []Record) error` — package-
  private, applies a mixed batch in log order. `Consumer`, `GroupConsumer`,
  and `Rebuild` all honor tombstones after this task. No exported API change.

- [ ] **Step 1: Write the failing tests**

Add to `pkg/ingest/consumer_test.go` (uses the file's existing `testRedis` and `ns` helpers):

```go
// TestConsumerAppliesTombstones: submit → remove → submit ordering within one
// drain, across all fan-out windows.
func TestConsumerAppliesTombstones(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	eng := engine.NewRedisEngine(rdb)
	app := ns(t)

	reg := NewStaticRegistry()
	lb := engine.LogicalBoard{App: app, Board: "score",
		Windows: []engine.WindowSpec{{Kind: engine.WindowAllTime}, {Kind: engine.WindowDaily}}}
	reg.Register(lb)
	log := NewMemLog()
	ing := NewIngestor(log, reg, NewMemDeduper())
	now := time.Now().UTC()
	t.Cleanup(func() { resetFanout(ctx, eng, app, now) })

	submit := func(m string, sc float64) {
		t.Helper()
		if acc, err := ing.Submit(ctx, Record{App: app, Board: "score", Member: m, Score: sc, Time: now}); err != nil || !acc {
			t.Fatalf("submit: %v/%v", acc, err)
		}
	}
	submit("alice", 300)
	submit("bob", 500)
	if err := ing.Remove(ctx, Record{App: app, Board: "score", Member: "alice", Time: now}); err != nil {
		t.Fatal(err)
	}
	submit("carol", 100) // after the tombstone; must survive

	cons := NewConsumer(log, reg, eng)
	if err := cons.Drain(ctx); err != nil {
		t.Fatal(err)
	}

	for _, win := range []string{"all", (engine.WindowSpec{Kind: engine.WindowDaily}).WindowID(now)} {
		b := engine.Board{Key: engine.BoardKey{App: app, Board: "score", Segment: "all", Window: win}}
		if _, err := eng.GetRank(ctx, b, "alice"); !errors.Is(err, engine.ErrMemberNotFound) {
			t.Errorf("alice still on window %s: %v", win, err)
		}
		if c, _ := eng.Count(ctx, b); c != 2 {
			t.Errorf("window %s count: got %d, want 2 (bob+carol)", win, c)
		}
	}

	// A submit AFTER the removal re-adds the member (remove is not a ban).
	submit("alice", 999)
	if err := cons.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	b := engine.Board{Key: engine.BoardKey{App: app, Board: "score", Segment: "all", Window: "all"}}
	if re, err := eng.GetRank(ctx, b, "alice"); err != nil || re.Rank != 1 {
		t.Errorf("alice after re-submit: %+v / %v", re, err)
	}
}

// TestRebuildReproducesRemoval is the core invariant: a rebuilt cache must
// not resurrect removed entries.
func TestRebuildReproducesRemoval(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	eng := engine.NewRedisEngine(rdb)
	app := ns(t)

	reg := NewStaticRegistry()
	lb := engine.LogicalBoard{App: app, Board: "score"}
	reg.Register(lb)
	log := NewMemLog()
	ing := NewIngestor(log, reg, NewMemDeduper())
	now := time.Now().UTC()
	b := engine.Board{Key: engine.BoardKey{App: app, Board: "score", Segment: "all", Window: "all"}}
	t.Cleanup(func() { _ = eng.Reset(ctx, b) })

	_, _ = ing.Submit(ctx, Record{App: app, Board: "score", Member: "cheater", Score: 9999, Time: now})
	_, _ = ing.Submit(ctx, Record{App: app, Board: "score", Member: "honest", Score: 100, Time: now})
	if err := ing.Remove(ctx, Record{App: app, Board: "score", Member: "cheater", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := NewConsumer(log, reg, eng).Drain(ctx); err != nil {
		t.Fatal(err)
	}

	// Simulate cache loss, then rebuild from the log.
	if err := eng.Reset(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := Rebuild(ctx, log, reg, eng); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.GetRank(ctx, b, "cheater"); !errors.Is(err, engine.ErrMemberNotFound) {
		t.Errorf("rebuild resurrected the removed member: %v", err)
	}
	if re, err := eng.GetRank(ctx, b, "honest"); err != nil || re.Rank != 1 {
		t.Errorf("honest member lost in rebuild: %+v / %v", re, err)
	}
}
```

Add to `pkg/ingest/group_test.go`, following that file's existing setup pattern for building a `RedisLog` + `GroupConsumer` (unique stream prefix via `ns(t)`, `EnsureGroups`, then `Step`; copy the arrangement of an existing GroupConsumer test in that file exactly, changing only the records fed and the assertions):

```go
func TestGroupConsumerAppliesTombstones(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	eng := engine.NewRedisEngine(rdb)
	app := ns(t)

	reg := NewStaticRegistry()
	reg.Register(engine.LogicalBoard{App: app, Board: "score"})
	rlog := NewRedisLog(rdb, "test:"+app, 1, 0)
	t.Cleanup(func() { rdb.Del(ctx, rlog.StreamName(0)) })
	ing := NewIngestor(rlog, reg, NewMemDeduper())
	now := time.Now().UTC()
	b := engine.Board{Key: engine.BoardKey{App: app, Board: "score", Segment: "all", Window: "all"}}
	t.Cleanup(func() { _ = eng.Reset(ctx, b) })

	_, _ = ing.Submit(ctx, Record{App: app, Board: "score", Member: "alice", Score: 300, Time: now})
	if err := ing.Remove(ctx, Record{App: app, Board: "score", Member: "alice", Time: now}); err != nil {
		t.Fatal(err)
	}

	gc := NewGroupConsumer(rlog, reg, eng, GroupOptions{Group: "g-" + app})
	if err := gc.EnsureGroups(ctx); err != nil {
		t.Fatal(err)
	}
	for {
		n, err := gc.Step(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	if _, err := eng.GetRank(ctx, b, "alice"); !errors.Is(err, engine.ErrMemberNotFound) {
		t.Errorf("tombstone not applied by group consumer: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `docker compose run --rm app go test ./pkg/ingest/... -count=1 -run "Tombstone|TestRebuildReproduces"`
Expected: FAIL — removals are ignored (`alice still on window ...` / `rebuild resurrected the removed member`), because the consumers still treat every record as a submit.

- [ ] **Step 3: Implement**

`pkg/ingest/consumer.go` — replace the `recordsToOps` function (lines 49-61) with:

```go
// applyRecords applies a mixed batch of submits and tombstones in log order:
// consecutive submits are batched into one SubmitBatch; a removal flushes the
// pending batch first (so earlier submits of the same member land before the
// removal), then removes the member from every live physical board. Records
// whose board is no longer registered are skipped.
func applyRecords(ctx context.Context, eng engine.RankingEngine, resolver BoardResolver, recs []Record) error {
	var ops []engine.SubmitOp
	flush := func() error {
		if len(ops) == 0 {
			return nil
		}
		_, err := eng.SubmitBatch(ctx, ops)
		ops = nil
		return err
	}
	for _, rec := range recs {
		lb, ok := resolver.Resolve(rec.App, rec.Board)
		if !ok {
			continue
		}
		if rec.Op == OpRemove {
			if err := flush(); err != nil {
				return err
			}
			if err := eng.RemoveFromAll(ctx, lb, rec.Member); err != nil {
				return err
			}
			continue
		}
		ops = append(ops, recordToOps(lb, rec)...)
	}
	return flush()
}
```

In `Consumer.Step`, replace

```go
		if ops := recordsToOps(c.resolver, recs); len(ops) > 0 {
			if _, err := c.eng.SubmitBatch(ctx, ops); err != nil {
				return total, err
			}
		}
```

with

```go
		if err := applyRecords(ctx, c.eng, c.resolver, recs); err != nil {
			return total, err
		}
```

`pkg/ingest/group.go` — in `GroupConsumer.apply`, replace the ops-building block (steps 2-3, currently lines 126-153) with record collection + `applyRecords`:

```go
	// 2. Collect not-yet-applied, resolvable records (in stream order).
	var recs []Record
	var newIDs []string
	allIDs := make([]string, len(msgs))
	for i, m := range msgs {
		allIDs[i] = m.ID
		if existsCmds[i].Val() > 0 {
			continue // already applied: skip, will still ACK
		}
		rec, ok, err := messageToRecord(m)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue // malformed: skip, will ACK
		}
		if _, ok := g.resolver.Resolve(rec.App, rec.Board); ok {
			recs = append(recs, rec)
			newIDs = append(newIDs, m.ID)
		}
	}

	// 3. Apply in order, then 4. mark applied (apply-before-mark = at-least-once).
	if err := applyRecords(ctx, g.eng, g.resolver, recs); err != nil {
		return 0, err
	}
```

(The `ops`/`SubmitBatch` block it replaces disappears; the marking and ACK steps 4-5 stay unchanged. Remove the now-unused `"github.com/kodeni-am/leaderboard/pkg/engine"` import from group.go **only if** nothing else in the file still references `engine.` — `NewGroupConsumer`'s signature does, so it stays.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `docker compose run --rm app go test ./pkg/ingest/... -count=1`
Expected: PASS (all ingest tests, old and new)

- [ ] **Step 5: Run the full suite (interface change touched engine + consumers)**

Run: `make test`
Expected: PASS everywhere (`ok` for every package; `pkg/api`, `pkg/sdk` etc. compile against the extended interface because both engines implement it)

- [ ] **Step 6: Commit**

```bash
git add pkg/ingest
git commit -m "ingest: consumers and rebuild apply removal tombstones in log order"
```

---

### Task 5: API endpoints — DELETE score entry, DELETE player

**Files:**
- Create: `pkg/api/moderation.go`
- Modify: `pkg/api/server.go` (two routes)
- Test: `pkg/api/moderation_test.go`

**Interfaces:**
- Consumes: `s.ing.Remove` (Task 3), `s.eng.RemoveFromAll` (Task 2),
  `s.users.Delete` (Task 1), `s.store.ListBoards`, `s.resolveBoard`,
  `s.registry.Register`, `requireApp` data-plane auth.
- Produces: `DELETE /v1/boards/{board}/scores/{member}` and
  `DELETE /v1/users/{id}` — 204 on success (idempotent), 404 unknown board,
  500 `removal_queued` when the tombstone is durable but the immediate apply
  failed. Used by Task 6 (dashboard) and Tasks 7-9 (SDKs).

- [ ] **Step 1: Write the failing test**

Create `pkg/api/moderation_test.go` (uses `newHarness`/`onboard`/`call`/`key`/`sess` from `server_test.go`):

```go
package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/kodeni-am/leaderboard/pkg/engine"
)

func TestRemoveScoreAndDeletePlayer(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "mod@example.com")
	ctx := context.Background()
	cleanup := func(board string) {
		for _, win := range []string{"all"} {
			_ = h.eng.Reset(ctx, engine.Board{Key: engine.BoardKey{App: h.appID, Board: board, Segment: "all", Window: win}})
		}
	}
	t.Cleanup(func() { cleanup("high"); cleanup("laps") })

	// Board + scores via API key; drain the write-behind consumer.
	if resp, body := h.call(t, http.MethodPost, "/v1/boards", h.key(), map[string]any{"board": "high"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create board: %d %s", resp.StatusCode, body)
	}
	for _, s := range []struct {
		m string
		v float64
	}{{"alice", 300}, {"bob", 500}} {
		if resp, body := h.call(t, http.MethodPost, "/v1/boards/high/scores", h.key(), map[string]any{"member": s.m, "score": s.v}); resp.StatusCode != http.StatusAccepted {
			t.Fatalf("submit: %d %s", resp.StatusCode, body)
		}
	}
	if err := h.cons.Drain(ctx); err != nil {
		t.Fatal(err)
	}

	// --- Remove one entry (API-key auth). 204, immediately gone from top. ---
	if resp, body := h.call(t, http.MethodDelete, "/v1/boards/high/scores/alice", h.key(), nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove score: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.call(t, http.MethodGet, "/v1/boards/high/top?n=10", h.key(), nil); resp.StatusCode != http.StatusOK || strings.Contains(string(body), "alice") {
		t.Fatalf("alice still in top after removal: %d %s", resp.StatusCode, body)
	}
	// Idempotent: removing an absent member is still 204.
	if resp, _ := h.call(t, http.MethodDelete, "/v1/boards/high/scores/alice", h.key(), nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("re-remove: %d", resp.StatusCode)
	}
	// Unknown board is 404.
	if resp, _ := h.call(t, http.MethodDelete, "/v1/boards/nope/scores/alice", h.key(), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown board: %d", resp.StatusCode)
	}
	// The removal survives a rebuild: reset the board, drain from scratch...
	// (the harness Consumer keeps cursors, so replay via a fresh Rebuild is
	// covered in pkg/ingest; here we assert bob is intact instead).
	if resp, body := h.call(t, http.MethodGet, "/v1/boards/high/rank?member=bob", h.key(), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("bob rank: %d %s", resp.StatusCode, body)
	}

	// --- Delete a registered player entirely (session auth + CSRF). ---
	var u struct {
		UserID string `json:"user_id"`
	}
	resp, body := h.call(t, http.MethodPost, "/v1/users", h.sess(), map[string]string{"nickname": "Ninja"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: %d %s", resp.StatusCode, body)
	}
	mustJSON(t, body, &u)
	if resp, body := h.call(t, http.MethodPost, "/v1/boards", h.key(), map[string]any{"board": "laps"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create board 2: %d %s", resp.StatusCode, body)
	}
	for _, board := range []string{"high", "laps"} {
		if resp, body := h.call(t, http.MethodPost, "/v1/boards/"+board+"/scores", h.key(), map[string]any{"member": u.UserID, "score": 777}); resp.StatusCode != http.StatusAccepted {
			t.Fatalf("submit %s: %d %s", board, resp.StatusCode, body)
		}
	}
	if err := h.cons.Drain(ctx); err != nil {
		t.Fatal(err)
	}

	if resp, body := h.call(t, http.MethodDelete, "/v1/users/"+u.UserID, h.sess(), nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete player: %d %s", resp.StatusCode, body)
	}
	// Gone from every board...
	for _, board := range []string{"high", "laps"} {
		if resp, body := h.call(t, http.MethodGet, "/v1/boards/"+board+"/top?n=10", h.key(), nil); strings.Contains(string(body), u.UserID) {
			t.Fatalf("player still on %s: %d %s", board, resp.StatusCode, body)
		}
	}
	// ...registration gone, nickname re-claimable.
	if resp, _ := h.call(t, http.MethodGet, "/v1/users/"+u.UserID, h.key(), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get deleted user: %d", resp.StatusCode)
	}
	if resp, body := h.call(t, http.MethodPost, "/v1/users", h.key(), map[string]string{"nickname": "Ninja"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("nickname not released: %d %s", resp.StatusCode, body)
	}
	// Deleting an unregistered raw member is still 204 (registry no-op).
	if resp, _ := h.call(t, http.MethodDelete, "/v1/users/bob", h.key(), nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete raw member: %d", resp.StatusCode)
	}
}

func TestModerationAuth(t *testing.T) {
	h := newHarness(t)
	h.onboard(t, "mod-auth@example.com")

	// No auth at all -> 401.
	if resp, _ := h.call(t, http.MethodDelete, "/v1/boards/high/scores/alice", map[string]string{"Authorization": "Bearer nope"}, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad key: %d", resp.StatusCode)
	}
	// Session without CSRF -> 403 (cookie jar carries the session).
	if resp, _ := h.call(t, http.MethodDelete, "/v1/users/whoever", map[string]string{"X-App-Id": h.appID}, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("missing csrf: %d", resp.StatusCode)
	}
}

// mustJSON unmarshals or fails the test.
func mustJSON(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
}
```

Add `"encoding/json"` to the test file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `docker compose run --rm app go test ./pkg/api/... -count=1 -run "TestRemoveScore|TestModerationAuth"`
Expected: FAIL — `remove score: 404` (route doesn't exist yet; the SPA fallback isn't configured in tests so unmatched routes 404)

- [ ] **Step 3: Implement**

Create `pkg/api/moderation.go`:

```go
package api

import (
	"net/http"

	"github.com/kodeni-am/leaderboard/pkg/ingest"
	"github.com/kodeni-am/leaderboard/pkg/tenancy"
)

// Moderation endpoints: durable removal of board entries and whole players.
// The log append is the commit point — replay and rebuild reproduce the
// deletion. The immediate engine apply only provides read-your-writes; if it
// fails after a successful append we answer removal_queued and the consumer
// converges from the tombstone.

func (s *Server) handleRemoveScore(w http.ResponseWriter, r *http.Request) {
	app, _ := tenancy.AppFromContext(r.Context())
	board := r.PathValue("board")
	member := r.PathValue("member")
	lb, err := s.resolveBoard(r.Context(), app.ID, board)
	if err != nil {
		writeErr(w, http.StatusNotFound, "unknown board")
		return
	}
	if err := s.ing.Remove(r.Context(), ingest.Record{App: app.ID, Board: board, Member: member}); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.eng.RemoveFromAll(r.Context(), lb, member); err != nil {
		writeErr(w, http.StatusInternalServerError, "removal_queued")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteUser deletes a player entirely: one removal tombstone per board
// (each lands in the same log partition as that board's submits, preserving
// replay order), then the registration. The registry is primary data — it
// never flows through the ingest log — so its deletion needs no tombstone,
// and users.Delete releases the nickname only if this player still owns it.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	app, _ := tenancy.AppFromContext(r.Context())
	member := r.PathValue("id")
	boards, err := s.store.ListBoards(r.Context(), app.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, lb := range boards {
		s.registry.Register(lb) // store-listed boards may not be warmed yet
		if err := s.ing.Remove(r.Context(), ingest.Record{App: app.ID, Board: lb.Board, Member: member}); err != nil {
			// Tombstones so far are durable and harmless; the client retries.
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	queued := false
	for _, lb := range boards {
		if err := s.eng.RemoveFromAll(r.Context(), lb, member); err != nil {
			queued = true // tombstone is durable; the consumer converges
		}
	}
	if err := s.users.Delete(r.Context(), app.ID, member); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if queued {
		writeErr(w, http.StatusInternalServerError, "removal_queued")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

`pkg/api/server.go` — add the routes: after `dataPlane("POST /v1/boards/{board}/scores", s.handleSubmit)` add

```go
	dataPlane("DELETE /v1/boards/{board}/scores/{member}", s.handleRemoveScore)
```

and after `dataPlane("PATCH /v1/users/{id}", s.handleRenameUser)` add

```go
	dataPlane("DELETE /v1/users/{id}", s.handleDeleteUser)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `docker compose run --rm app go test ./pkg/api/... -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/api
git commit -m "api: DELETE endpoints to remove a board entry and delete a player"
```

---

### Task 6: Dashboard — remove entry / delete player from the board viewer

**Files:**
- Modify: `web/src/api.ts` (two client functions)
- Modify: `web/src/pages/Dashboard.tsx` (`Viewer` + `RankSearch`)

**Interfaces:**
- Consumes: Task 5's endpoints; existing `req`/`appHdr` helpers, `ConfirmDialog`
  component (already imported in Dashboard.tsx), `ApiError`.
- Produces: `api.removeScore(appId, board, member)` and
  `api.deleteUser(appId, member)`; per-row actions in the top-N table and a
  delete-player action on rank-search results.

- [ ] **Step 1: Add the API client functions**

In `web/src/api.ts`, add to the `api` object after `neighbors`:

```ts
  removeScore: (appId: string, board: string, member: string) =>
    req<unknown>("DELETE", `/v1/boards/${encodeURIComponent(board)}/scores/${encodeURIComponent(member)}`, undefined, appHdr(appId)),
  deleteUser: (appId: string, member: string) =>
    req<unknown>("DELETE", `/v1/users/${encodeURIComponent(member)}`, undefined, appHdr(appId)),
```

- [ ] **Step 2: Wire the Viewer UI**

In `web/src/pages/Dashboard.tsx`, inside `Viewer` (after the `busy` state), add:

```tsx
  const [confirmState, setConfirmState] = useState<{
    title: string;
    body: string;
    label: string;
    onYes: () => Promise<void>;
  } | null>(null);
  const danger = { borderColor: "var(--danger)", color: "var(--danger)" };

  // The server answers removal_queued when the removal is durably logged but
  // the immediate apply failed — the consumer finishes it shortly.
  function friendly(e: unknown): string {
    const err = e as ApiError;
    return err.message === "removal_queued" ? "Removal queued — it may take a moment to apply." : err.message;
  }

  function askRemoveEntry(entry: RankEntry) {
    const who = entry.nickname ? `${entry.nickname} (${entry.member})` : entry.member;
    setConfirmState({
      title: "Remove entry?",
      body: `Remove ${who} from ${board}? This removes their entry from every window and segment of this board. They can submit again afterwards.`,
      label: "Remove entry",
      onYes: async () => {
        await api.removeScore(appId, board, entry.member);
        await loadTop();
      },
    });
  }

  function askDeletePlayer(member: string, nickname?: string) {
    const who = nickname ? `${nickname} (${member})` : member;
    setConfirmState({
      title: "Delete player?",
      body: `Delete ${who} entirely? This removes their scores from ALL boards in this app and releases their nickname. This can't be undone.`,
      label: "Delete player",
      onYes: async () => {
        await api.deleteUser(appId, member);
        await loadTop();
      },
    });
  }
```

Change the table header row to add an empty actions column:

```tsx
            <thead><tr><th>Rank</th><th>Player</th><th style={{ textAlign: "right" }}>Score</th><th /></tr></thead>
```

Add an actions cell to each row, after the score `<td>`:

```tsx
                  <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                    <button
                      className="btn btn-ghost btn-sm"
                      style={danger}
                      title="Remove this entry from every window/segment of this board"
                      onClick={() => askRemoveEntry(e)}
                    >
                      Remove
                    </button>{" "}
                    <button
                      className="btn btn-ghost btn-sm"
                      style={danger}
                      title="Delete this player: all scores on all boards, nickname released"
                      onClick={() => askDeletePlayer(e.member, e.nickname)}
                    >
                      Delete player
                    </button>
                  </td>
```

Pass the delete action into RankSearch (change the existing element):

```tsx
      <RankSearch appId={appId} board={board} window={win} segment={seg} onDeletePlayer={askDeletePlayer} />
```

Render the dialog just before `Viewer`'s closing `</div>`:

```tsx
      {confirmState && (
        <ConfirmDialog
          title={confirmState.title}
          body={confirmState.body}
          confirmLabel={confirmState.label}
          danger
          onCancel={() => setConfirmState(null)}
          onConfirm={async () => {
            const fn = confirmState.onYes;
            setConfirmState(null);
            try {
              await fn();
            } catch (e) {
              setErr(friendly(e));
            }
          }}
        />
      )}
```

- [ ] **Step 3: Add the RankSearch delete action**

Change `RankSearch`'s signature and add a button next to the result:

```tsx
function RankSearch({ appId, board, window, segment, onDeletePlayer }: { appId: string; board: string; window: string; segment: string; onDeletePlayer?: (member: string, nickname?: string) => void }) {
```

Inside the result render branch (the `{result ? (...) : (...)}` expression), extend the result case to:

```tsx
          <span>
            {result.nickname ? `${result.nickname} · ` : ""}
            <span className="accent">#{result.rank}</span> · {result.score.toLocaleString()}
            {onDeletePlayer && (
              <>
                {" "}
                <button
                  type="button"
                  className="btn btn-ghost btn-sm"
                  style={{ borderColor: "var(--danger)", color: "var(--danger)" }}
                  title="Delete this player: all scores on all boards, nickname released"
                  onClick={() => onDeletePlayer(result.member, result.nickname)}
                >
                  Delete
                </button>
              </>
            )}
          </span>
```

- [ ] **Step 4: Verify the build**

Run: `npm --prefix web run build`
Expected: `tsc --noEmit` clean, vite build succeeds

- [ ] **Step 5: Verify in the running app**

Run: `docker compose up --build -d leaderboardd`, open the dashboard, create/select a board with entries, and exercise: Remove (row disappears immediately), Delete player (gone from all boards; nickname reusable via the register form). Then `docker compose down`.
Expected: both confirm dialogs appear; the table refreshes without the removed rows; no console errors.

- [ ] **Step 6: Commit**

```bash
git add web/src
git commit -m "dashboard: remove entry and delete player actions in the board viewer"
```

---

### Task 7: Go SDK — RemoveScore, DeleteUser

**Files:**
- Modify: `pkg/sdk/client.go`
- Test: `pkg/sdk/client_test.go`

**Interfaces:**
- Consumes: Task 5's endpoints; the SDK's `do` helper.
- Produces: `(*Client).RemoveScore(ctx, board, member string) error`,
  `(*Client).DeleteUser(ctx, id string) error`.

- [ ] **Step 1: Write the failing test**

Add to `pkg/sdk/client_test.go` (mirror `TestSDKAgainstServer`'s setup — Redis skip-check, in-process API server, MemStore tenancy, MemLog + Consumer):

```go
func TestSDKModeration(t *testing.T) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewUniversalClient(&redis.UniversalOptions{Addrs: []string{addr}})
	ctx := context.Background()
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := rdb.Ping(pctx).Err(); err != nil {
		t.Skipf("redis not available: %v", err)
	}

	eng := engine.NewRedisEngine(rdb)
	store := tenancy.NewMemStore()
	registry := ingest.NewStaticRegistry()
	log := ingest.NewMemLog()
	ing := ingest.NewIngestor(log, registry, ingest.NewMemDeduper())
	cons := ingest.NewConsumer(log, registry, eng)
	srv := api.NewServer(eng, ing, store, registry, nil, false, users.NewMemStore())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	app, key, err := store.CreateApp(ctx, "usr_sdk_mod", "ModGame")
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

	u, err := c.RegisterUser(ctx, "Ninja")
	if err != nil {
		t.Fatal(err)
	}
	for m, sc := range map[string]float64{u.UserID: 900, "raw-alice": 500} {
		if _, err := c.Submit(ctx, "high", Submission{Member: m, Score: sc}); err != nil {
			t.Fatal(err)
		}
	}
	if err := cons.Drain(ctx); err != nil {
		t.Fatal(err)
	}

	// RemoveScore: entry gone, member can still be re-submitted.
	if err := c.RemoveScore(ctx, "high", "raw-alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetRank(ctx, "high", "raw-alice", QueryOpts{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("raw-alice still ranked: %v", err)
	}
	// Idempotent.
	if err := c.RemoveScore(ctx, "high", "raw-alice"); err != nil {
		t.Fatalf("re-remove: %v", err)
	}

	// DeleteUser: scores gone, registration gone, nickname re-claimable.
	if err := c.DeleteUser(ctx, u.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetRank(ctx, "high", u.UserID, QueryOpts{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted player still ranked: %v", err)
	}
	if _, err := c.GetUser(ctx, u.UserID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted player still registered: %v", err)
	}
	if _, err := c.RegisterUser(ctx, "Ninja"); err != nil {
		t.Fatalf("nickname not released: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `docker compose run --rm app go test ./pkg/sdk/... -count=1 -run TestSDKModeration`
Expected: FAIL to compile — `c.RemoveScore undefined`, `c.DeleteUser undefined`

- [ ] **Step 3: Implement**

Add to `pkg/sdk/client.go` after `RenameUser`:

```go
// RemoveScore removes a member's entry from a board — every window and
// segment. The removal is durably logged and survives cache rebuilds.
// Removing an absent member is a no-op; the member may submit again
// afterwards. Returns ErrNotFound for an unknown board.
func (c *Client) RemoveScore(ctx context.Context, board, member string) error {
	_, err := c.do(ctx, http.MethodDelete, "/v1/boards/"+url.PathEscape(board)+"/scores/"+url.PathEscape(member), nil, nil)
	return err
}

// DeleteUser deletes a player entirely: their scores on every board in the
// app plus their registration — the nickname is released for re-use. Works
// for unregistered raw member ids too (the registry step is then a no-op).
func (c *Client) DeleteUser(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/v1/users/"+url.PathEscape(id), nil, nil)
	return err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `docker compose run --rm app go test ./pkg/sdk/... -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/sdk
git commit -m "Go SDK: RemoveScore and DeleteUser"
```

---

### Task 8: TypeScript SDK — removeScore, deleteUser (v0.5.0)

**Files:**
- Modify: `sdk/typescript/src/index.ts`
- Modify: `sdk/typescript/package.json` (version 0.4.0 → 0.5.0)
- Modify: `sdk/typescript/test/e2e.mjs`
- Rebuild: `sdk/typescript/dist/` (committed artifacts)

**Interfaces:**
- Consumes: Task 5's endpoints; the SDK's `send`/`enc` helpers.
- Produces: `removeScore(board, member): Promise<void>`,
  `deleteUser(userId): Promise<void>` on `LeaderboardClient`.

- [ ] **Step 1: Add the methods**

In `sdk/typescript/src/index.ts`, add after `renameUser`:

```ts
  /**
   * Remove a member's entry from a board — every window and segment. The
   * removal is durably logged and survives cache rebuilds. Removing an
   * absent member is a no-op; the member may submit again afterwards.
   * Throws {@link NotFoundError} for an unknown board.
   */
  async removeScore(board: string, member: string): Promise<void> {
    await this.send("DELETE", `/v1/boards/${enc(board)}/scores/${enc(member)}`);
  }

  /**
   * Delete a player entirely: their scores on every board in the app plus
   * their registration — the nickname is released for re-use. Works for
   * unregistered raw member ids too.
   */
  async deleteUser(userId: string): Promise<void> {
    await this.send("DELETE", `/v1/users/${enc(userId)}`);
  }
```

- [ ] **Step 2: Bump the version**

In `sdk/typescript/package.json` change `"version": "0.4.0"` to `"version": "0.5.0"`.

- [ ] **Step 3: Extend the e2e test**

In `sdk/typescript/test/e2e.mjs`, add before the final success output (after the existing nickname assertions, reusing the `lb`/`assert` bindings and the file's sleep pattern):

```js
// Moderation: remove one entry, then delete a player entirely.
await lb.submitScore("high", "mallory", 9999);
await new Promise((res) => setTimeout(res, 1500));
await lb.removeScore("high", "mallory");
let removed = false;
try {
  await lb.getRank("high", "mallory");
} catch (e) {
  removed = e instanceof NotFoundError;
}
assert(removed, "mallory removed from board");
await lb.removeScore("high", "mallory"); // idempotent

const victim = await lb.registerUser("ToDelete-" + Date.now());
await lb.submitScore("high", victim.user_id, 123);
await new Promise((res) => setTimeout(res, 1500));
await lb.deleteUser(victim.user_id);
let unregistered = false;
try {
  await lb.getUser(victim.user_id);
} catch (e) {
  unregistered = e instanceof NotFoundError;
}
assert(unregistered, "deleted player unregistered");
```

- [ ] **Step 4: Build and typecheck**

Run: `npm --prefix sdk/typescript run build`
Expected: clean compile; `sdk/typescript/dist/index.js` and `index.d.ts` regenerate and now contain `removeScore`/`deleteUser`.

(The e2e script needs a running server + `LB_API_KEY`; it self-skips otherwise. If a server from Task 6 verification is still up with a key handy, run `LB_API_KEY=<key> npm --prefix sdk/typescript run test:e2e`.)

- [ ] **Step 5: Commit**

```bash
git add sdk/typescript/src sdk/typescript/dist sdk/typescript/package.json sdk/typescript/test
git commit -m "TS SDK 0.5.0: removeScore and deleteUser"
```

---

### Task 9: Unity SDK — RemoveScoreAsync, DeleteUserAsync (v0.3.0)

**Files:**
- Modify: `sdk/unity/Runtime/LeaderboardClient.cs`
- Modify: `sdk/unity/package.json` (version 0.2.0 → 0.3.0)

**Interfaces:**
- Consumes: Task 5's endpoints; the client's `SendAsync`/`Esc` helpers.
- Produces: `Task RemoveScoreAsync(string board, string member)`,
  `Task DeleteUserAsync(string userId)`.

- [ ] **Step 1: Add the methods**

In `sdk/unity/Runtime/LeaderboardClient.cs`, add after `RenameUserAsync`:

```csharp
        /// <summary>
        /// Remove a member's entry from a board — every window and segment.
        /// The removal is durably logged and survives cache rebuilds.
        /// Removing an absent member is a no-op; the member may submit again
        /// afterwards. Throws <see cref="NotFoundException"/> for an unknown board.
        /// </summary>
        public async Task RemoveScoreAsync(string board, string member)
        {
            await SendAsync("DELETE", "/v1/boards/" + Esc(board) + "/scores/" + Esc(member), null);
        }

        /// <summary>
        /// Delete a player entirely: their scores on every board in the app
        /// plus their registration — the nickname is released for re-use.
        /// Works for unregistered raw member ids too.
        /// </summary>
        public async Task DeleteUserAsync(string userId)
        {
            await SendAsync("DELETE", "/v1/users/" + Esc(userId), null);
        }
```

- [ ] **Step 2: Bump the version**

In `sdk/unity/package.json` change `"version": "0.2.0"` to `"version": "0.3.0"`.

- [ ] **Step 3: Sanity check**

No Unity compiler is available in this repo; verify by inspection that the two methods only use existing helpers (`SendAsync`, `Esc`) and follow the exact shape of `RenameUserAsync`. Run `git diff sdk/unity` and re-read the hunk.

- [ ] **Step 4: Commit**

```bash
git add sdk/unity
git commit -m "Unity SDK 0.3.0: RemoveScoreAsync and DeleteUserAsync"
```

---

### Task 10: Documentation and spec status

**Files:**
- Modify: `README.md` (features list)
- Modify: `docs/superpowers/specs/2026-07-10-score-and-player-removal-design.md` (status)
- Modify: `sdk/typescript/README.md`, `sdk/unity/README.md`, `web/README.md` — only where they enumerate API methods/features (inspect each; skip any that don't)

**Interfaces:**
- Consumes: everything shipped in Tasks 1-9.
- Produces: user-facing docs that mention moderation/removal.

- [ ] **Step 1: README feature bullet**

In `README.md`, add to the `## Features` list (after the anti-cheat bullet):

```markdown
- **Moderation:** remove a member's entry from a board, or delete a player
  entirely (all boards + registered nickname) — from the dashboard, the API
  (`DELETE /v1/boards/{board}/scores/{member}`, `DELETE /v1/users/{id}`), or
  any SDK. Removals are durable tombstones in the ingest log, so they survive
  cache rebuilds.
```

- [ ] **Step 2: Mark the spec implemented**

In `docs/superpowers/specs/2026-07-10-score-and-player-removal-design.md`, change `**Status:** Approved for implementation` to `**Status:** Implemented`.

- [ ] **Step 3: SDK READMEs**

Check `sdk/typescript/README.md` and `sdk/unity/README.md` for a method list or usage example section; if the user-registry methods are documented there, add matching one-line entries/examples for `removeScore`/`deleteUser` (TS) and `RemoveScoreAsync`/`DeleteUserAsync` (Unity), following the exact formatting of the neighboring entries. If a README has no such list, leave it.

- [ ] **Step 4: Full suite + commit**

Run: `make test`
Expected: PASS

```bash
git add README.md docs sdk/typescript/README.md sdk/unity/README.md
git commit -m "Document score and player removal; mark spec implemented"
```
