# Player Counts — Design

**Date:** 2026-07-17
**Status:** Approved

## Goal

The dashboard's viewer shows a board's top 25 with no sense of scale — 25 of
what? This feature surfaces two counts:

- **Board depth:** how many players are on the board *as currently filtered*
  (window + segment), rendered in the viewer header as `TOP 25 OF 1,432`.
- **Playerbase size:** how many players are registered in the app, rendered
  beside the app selector as `5,204 players`.

Both are read-only displays over data the system already maintains.

## Decisions (from brainstorming)

- Both counts, not one: board depth contextualizes the ranking; playerbase
  size answers a different question (how big is the app) and is app-scoped,
  so it lives in the dashboard chrome rather than the board panel.
- Board depth comes from `engine.Count`, which **already exists** on the
  `RankingEngine` interface with exact implementations on both engines. Only
  the HTTP/client/UI layers are new.
- Board depth gets its own endpoint rather than being folded into `/top`'s
  response: `/top` is the hot path for game clients, and adding a `ZCARD` to
  every call taxes it to save the dashboard one request.
- Playerbase count is **owner-plane** (`/v1/apps/{id}/stats`), not
  `/v1/users/count` — see "Why not /v1/users/count" below.
- Out of scope: SDK methods (Go and TS), caching, historical/time-series
  counts, per-board registered-player breakdowns.

## Architecture

### Engine

No changes. `RankingEngine.Count(ctx, b) (int64, error)` already exists:

- `RedisEngine.Count` → `ZCARD` on the physical board key. O(1), exact, and
  correct for approx-rank boards too (they maintain the sorted set alongside
  the histogram).
- `ShardedEngine.Count` → sums per-shard `ZCARD`s. Exact, since a member
  routes to exactly one shard.

Because the count is taken on the *physical* board (`BoardKey` includes
segment and window), filtering falls out of the existing key construction.

### Users store

`users.Store` gains:

```go
// Count returns the number of registered players in the app. Zero for an
// unknown app (an app with no registrations is indistinguishable from one
// that does not exist, and this is not an authorization boundary).
Count(ctx context.Context, appID string) (int64, error)
```

- `RedisStore.Count` → `HLEN` on `plr:{app}:names`. O(1) and exact: the
  names hash holds exactly one field per registered player, and `Create`
  (`HSET`), `Delete` (`HDEL`), and `Rename` (`HSET` on the same field) all
  maintain it. No new index or write-path cost.
- `MemStore.Count` → length of the per-app user map, under its existing lock.

### API

Two new routes:

| Endpoint                          | Plane      | Response                        |
|-----------------------------------|------------|---------------------------------|
| `GET /v1/boards/{board}/count`    | data       | `200 {"count": 1432}`           |
|                                   |            | `404` unknown board             |
| `GET /v1/apps/{id}/stats`         | owner      | `200 {"players": 5204}`         |
|                                   |            | `404` unknown/unowned app       |

`handleCount` reuses `readBoard` (which resolves the logical board, 404s on
failure, and applies `segment`/`window` via `physicalBoard`), then calls
`eng.Count`. It accepts the same query params as `/top` by construction.

`handleAppStats` is registered with `user(...)` beside the other
`/v1/apps/{id}/...` routes, resolves the app via `s.ownedApp(w, r)` — the
same helper `handleGetSigning` and friends use, which handles the 404 — and
calls `users.Count`. The response is an object with a
`players` field rather than a bare `count`, so app-level stats have an
obvious home if more are added later.

#### Why not `/v1/users/count`

The obvious path collides with the existing `GET /v1/users/{id}`. Go's
`ServeMux` gives a literal segment precedence over a wildcard, so
`/v1/users/count` would always win — and a player who claimed the member id
`count` (which `normalizeMemberID` permits: any 1–64 char string not starting
with `plr_`) would silently become unreadable through that route. Putting the
count under `/v1/apps/{id}/stats` avoids the shadow entirely (nothing
wildcards at that depth, beside `keys` and `signing`) and keeps a one-call
playerbase-size metric on the operator plane instead of granting it to every
API-key holder.

The cost: game clients (API key, no session) cannot read the playerbase
count. Accepted — the requirement is a dashboard display. A data-plane route
can be added if a game needs the number in-client.

### Dashboard (`web/src`)

`api.ts` gains:

- `count(appId, board, q?)` → `{count: number}` (same `appHdr` + `qs`
  pattern as `top`).
- `appStats(appId)` → `{players: number}` (session-authed, app id in path,
  same shape as `getSigning`).

`Viewer`:

- New `count: number | null` state, fetched inside `loadTop` alongside
  `api.top` with the same `window`/`segment` arguments, so one Refresh (or
  Enter, or a test submit) updates both and they can never disagree about
  which view they describe.
- Header renders `TOP {entries.length} OF {count.toLocaleString()}` only when
  `count !== null && count > entries.length`; otherwise it stays `TOP
  {entries.length}` as today. This avoids the nonsense `TOP 3 OF 3` on small
  boards and degrades to current behavior when the count is unavailable.
- The existing window/segment suffixes (`· Daily · region=eu`) are unchanged.

`Dashboard`:

- Owns `playerCount` state and a `reloadPlayerCount` function, fetched on app
  change (and cleared while switching, so the previous app's number never
  shows against the new one).
- Renders `{playerCount.toLocaleString()} players` beside the app selector,
  and nothing at all when the count is null.
- Passes `reloadPlayerCount` down as `onPlayersChanged` to the two actions
  that mutate the registry: `TestSubmit`'s Register form and the Delete
  player confirmations (both the table's and `RankSearch`'s). This is three
  prop hops (`Dashboard → AppWorkspace → Viewer → TestSubmit`); accepted over
  a stale number on a surface whose purpose is testing registration.

### Error handling

Both counts are auxiliary, following the `enrichEntries` precedent: a failed
count sets state to null and renders without the number. No error banner, no
failed leaderboard — a count outage must never cost the user their ranks.

## Testing (Docker-only via `make test`; web via `npm --prefix web run build`)

- **Users store** (both `MemStore` and `RedisStore`, via the `testStore`
  conformance suite): fresh app → 0; after N creates → N; after a delete →
  N-1; after a rename → unchanged (the rename must not double-count or drop
  the field); counts are isolated per app.

  These assertions must run against their **own app namespace** (e.g.
  `app+"cnt"`), not the `app` the rest of the suite shares. `testStore`
  accumulates users across its sections, so counting the shared app would
  couple the expected numbers to suite ordering and break whenever a case is
  added above it.
- **API — board count:** after submits + consumer drain, `/count` matches the
  number submitted; a `segment=` filter counts only that segment; a `window=`
  filter counts only that window; unknown board → 404; empty board → 0;
  requires app auth.
- **API — app stats:** returns the registered-player count; reflects a
  delete; unknown/unowned app → 404; session required (an API key alone must
  not reach it).
- **Web:** `npm --prefix web run build` clean.

## Documentation

Both endpoints get rows in the README API table, matching the treatment
`/segments` received.
