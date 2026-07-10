# Segment Listing — Design

**Date:** 2026-07-10
**Status:** Approved for implementation

## Goal

Let app owners see which segments actually exist on a board. Segments are
ad-hoc — set per submit (e.g. `region=eu`), never declared on the board — so
today the dashboard's segment filter is free text with no discovery. This
feature adds enumeration of the segments currently live in the cache, via an
API endpoint and dashboard suggestions.

## Decisions (from brainstorming)

- Surface: API endpoint + dashboard. SDK methods are out of scope (the
  endpoint is SDK-ready for a follow-up).
- Enumeration mechanism: **on-demand SCAN** of the board's live physical
  keys (Approach A). No write-path cost, no new data structure; the list is
  by construction exactly what is queryable right now.
- Accepted semantics: a segment whose only keys lived in dated windows
  disappears after the reaper ages them out. Boards with an all-time window
  (the default) retain every segment ever used.
- Out of scope: SDK methods, response caching, segment deletion/cleanup.

## Architecture

### Engine

`RankingEngine` gains:

```go
// Segments returns the deduplicated, lexically sorted segment names that
// currently have live physical boards for lb — including "all", the segment
// unsegmented submits land in. An empty board yields an empty slice.
Segments(ctx context.Context, lb LogicalBoard) ([]string, error)
```

- `RedisEngine.Segments` reuses `scanBoardKeys` (which already glob-escapes
  the app/board components and parses `lb:{app:board:segment:window}:z` hash
  tags back into `BoardKey`s), dedupes the `Segment` fields, and sorts.
- `ShardedEngine.Segments` unions the scan across its shard-suffixed board
  names (`board#s<i>` for i in 0..shards), since each shard holds its own
  physical keys. Implementation may generalize the scan helper or loop the
  per-shard board names — either is fine; behavior is the union.
- Like `RemoveFromAll` and the reaper, the SCAN has single-node scope on
  Redis Cluster (existing, documented precedent).

### API

`GET /v1/boards/{board}/segments` on the data plane (`requireApp`),
registered beside the other board reads:

| Endpoint                            | Response                                    |
|-------------------------------------|---------------------------------------------|
| `GET /v1/boards/{board}/segments`   | `200 {"segments": ["all", "region=eu", …]}` |
|                                     | `404` unknown board                          |

The handler resolves the board via `resolveBoard` (404 on failure) and calls
`eng.Segments`. The `segments` field is always a JSON array — an empty
result marshals as `[]`, never `null` (initialize the slice).

### Dashboard (`web/src`)

- `api.ts` gains `segments(appId, board)` → the GET above (same `appHdr`
  pattern as `top`).
- In `Viewer`, the segment filter input gains a `<datalist>` — the same
  pattern as the window input beside it. The list is fetched when the board
  changes (alongside the initial `loadTop`) and feeds suggestions only:
  blank still means "all segments", and free typing still works (the list
  does not validate). The `"all"` entry is shown like any other segment.
- A fetch failure leaves the datalist empty (suggestions are auxiliary —
  same best-effort stance as nickname enrichment).

## Testing (Docker-only via `make test`; web via `npm --prefix web run build`)

- **Engine:** submits across two segments and two windows → deduped, sorted
  list including `"all"`; a segment present only in a past (pre-reaper)
  window is still listed; a board name containing glob metacharacters lists
  only its own segments (escaped-scan reuse); empty board → empty slice.
- **API:** after submits + consumer drain, the endpoint returns the expected
  array; unknown board → 404; fresh board → `{"segments":[]}` (JSON array,
  not null).
- **Web:** `npm --prefix web run build` clean.
