# Score & Player Removal — Design

**Date:** 2026-07-10
**Status:** Implemented

## Goal

Let app owners moderate their leaderboards from the dashboard (and
programmatically via API key / SDKs) with two distinct actions:

1. **Remove entry** — delete one member's entry from one logical board.
2. **Delete player** — delete a member entirely: all scores on all boards,
   plus their registration (nickname released; the member may re-register
   later as a fresh player).

Both actions must be **rebuild-safe**: the durable ingest log is the source
of truth and the Redis ranking tier is a rebuildable cache, so a deletion
that only touches Redis would be resurrected by rebuild-from-log or by the
window reaper. Deletions are therefore recorded as durable tombstone events.

## Decisions (from brainstorming)

- Two separate actions: remove-entry and delete-player.
- Remove-entry reaches **all windows and segments** of the board — a bogus
  score is bogus everywhere.
- Delete-player frees the nickname and does **not** ban the member ID; they
  may re-register and submit again.
- Auth: standard data plane (`requireApp`) — app API key, or dashboard
  session + `X-App-Id` + CSRF. API-key holders can already submit arbitrary
  scores, so this grants no new trust.
- SDKs (Go, TypeScript, Unity) expose both operations.
- Out of scope: ban lists, moderation audit log.

## Architecture

### Tombstone events in the ingest log

`ingest.Record` gains an `Op` field (JSON `op`, `omitempty`):

| Op            | Meaning                                              | Fields used        |
|---------------|------------------------------------------------------|--------------------|
| "" (`submit`) | score submission (default; all existing log entries) | all (unchanged)    |
| `remove`      | remove one member's entry from one logical board     | App, Board, Member |

- Absent/empty `op` decodes as a submit, so the log format is backward
  compatible; encode/decode stays plain JSON in the stream's `d` field.
- Score/Segments are unused on tombstones.
- **There is no `deletePlayer` log op.** The log partitions by
  (app, board, member): a per-board `remove` tombstone lands in the same
  partition as that member's submits on that board, so replay order is
  preserved. A single cross-board delete event would land in a different
  partition and race its own submits. "Delete player" therefore decomposes at
  the API layer into one `remove` tombstone per board plus a synchronous
  registry deletion (see Player deletion below).
- Redelivery is safe without a dedup key: removal is naturally idempotent,
  and the GroupConsumer's applied-id markers already dedup redelivered
  stream entries.

### Write path (API handler)

1. **Append the tombstone to the durable log — this is the commit point.**
   If the append fails, the request fails and nothing is applied.
2. Apply the removal to Redis immediately via the same shared apply function
   the consumer uses, so the dashboard gets read-your-writes.
3. Return `204 No Content`.

If step 2 fails after a successful append, return HTTP 500; the tombstone is
durable and the consumer will converge. The dashboard surfaces this specific
case as "removal queued — may take a moment".

The consumer later applies the same tombstone again; removal is naturally
idempotent (`ZREM` of an absent member is a no-op; the histogram decrement is
guarded by the `ZSCORE` lookup, which finds nothing the second time).

### Replay / rebuild

A shared `applyRecords` function (used by both `Consumer` and
`GroupConsumer`) applies a mixed batch in log order:

- Consecutive submits batch into `SubmitBatch` as today.
- A `remove` tombstone **flushes the pending submit batch first** (so earlier
  submits of the same member land before the removal), then calls the engine
  removal (below).

Rebuild-from-log (`Rebuild` → `Consumer.Drain`) therefore reproduces every
deletion at its position in history. A `remove` is not a ban: submits later
in the log (or new live submits) re-add the member, which is the intended
semantics.

### Engine fan-out

`engine.Remove` operates on one *physical* board; a submit fans out to
`windows × segments` physical boards. The `RankingEngine` interface gains

```go
RemoveFromAll(ctx context.Context, lb LogicalBoard, member string) error
```

which removes the member from **every physical board currently live** for the
logical board — all segments and all windows, including past windows the
reaper has not yet aged out. Implementation: SCAN for `lb:{app:board:*}:z`
keys and parse the hash tag back into `BoardKey`s (components cannot contain
`:`, so the parse is unambiguous — same trick as the reaper's
`windowFromZKey`), then call `Remove` on each. Like the reaper's sweep, the
SCAN inherits single-node scope on Redis Cluster (existing precedent).

`RedisEngine.Remove` already maintains the sorted set and the approx-rank
histogram correctly. `ShardedEngine.RemoveFromAll` scans only the member's
shard (`board#s<shardOf(member)>` suffix) since writes route by member.

This method runs in both the API handler (immediate apply) and the consumers
(replay).

### Player deletion

`DELETE /v1/users/{member}` in the API handler:

1. Lists the app's boards from the tenancy store.
2. Appends one `remove` tombstone per board (durable commit point), then
   applies `RemoveFromAll` per board for read-your-writes.
3. Calls a new `users.Delete(appID, member)` that removes the registration
   and frees the nickname. No log event: the registry is primary data (it
   never flows through the ingest log and is not rebuilt from it).

Deleting an absent user is a no-op, keeping the endpoint idempotent and
retryable after partial failure.

**Re-register safety:** player ids are never reused (every registration
mints a fresh `plr_` id), so a delete can never affect a later
re-registration's record. The only shared resource is the nickname:
`users.Delete` releases the lowercased nickname claim **only if it still
maps to this id** (same atomic-Lua-with-retry pattern as `users.Rename`),
so a delete racing a rename or a re-claimed nickname stays correct.

## API

Both endpoints on the data plane (`requireApp`):

| Endpoint                                  | Action                                   | Responses |
|-------------------------------------------|------------------------------------------|-----------|
| `DELETE /v1/boards/{board}/scores/{member}` | remove entry (all windows & segments)    | 204; 404 unknown board |
| `DELETE /v1/users/{member}`               | delete player (all boards + registry)    | 204 |

`204` is returned even when the member had no entry / no registration —
deletion is idempotent. Dashboard-session calls require CSRF, as with all
existing data-plane mutations.

## Dashboard UI (`web/src`)

- **Per-row remove** in the `Viewer` top-N table: a remove action per row
  opens the existing `ConfirmDialog` destructive pattern (as used by
  key-revoke/app-delete): "Remove *nickname/member* from *board*? This
  removes their entry from every window and segment of this board." On
  confirm, call `api.removeScore(appId, board, member)` and refresh.
- **Delete player** in the same row menu and in `RankSearch` results, with a
  stronger confirmation spelling out that all scores across **all boards**
  and the registration/nickname are removed. Calls
  `api.deleteUser(appId, member)` (named to match the existing
  `registerUser` in `api.ts`).
- New `api.ts` functions follow the `deleteApp` pattern (DELETE, `appHdr`,
  CSRF handled by `req()`).
- Errors surface as toasts; the append-succeeded/apply-failed case shows
  "removal queued — may take a moment".

## SDKs

Follow each SDK's existing naming and error conventions:

| SDK        | Location                                  | Methods |
|------------|-------------------------------------------|---------|
| Go         | `pkg/sdk/client.go`                       | `RemoveScore(ctx, board, member string) error`, `DeleteUser(ctx, id string) error` |
| TypeScript | `sdk/typescript/src/index.ts`             | `removeScore(board, member)`, `deleteUser(userId)` |
| Unity      | `sdk/unity/Runtime/LeaderboardClient.cs`  | `RemoveScoreAsync(board, member)`, `DeleteUserAsync(userId)` |

TS SDK gets a minor version bump; README/docs snippets updated alongside.

## Testing (Docker-only via `make test`)

- **Engine:** fan-out removal clears every window/segment key including
  histogram buckets on approx boards; stale (pre-reaper) window keys cleaned.
- **Ingest:** tombstone encode/decode round-trip; records without `op`
  decode as submits; consumers apply `remove`; `applyRecords` preserves
  submit→remove→submit ordering within a batch; redelivery is idempotent.
- **Rebuild:** submit → remove → rebuild-from-log → member absent (the core
  invariant this design exists for).
- **Users:** `Delete` releases the nickname for re-claim, is a no-op on
  unknown ids, and never releases a nickname that has been re-claimed by a
  different player.
- **API:** both auth paths (API key; session + CSRF); 404/204 semantics;
  log-append failure fails the request.
- **Dashboard:** removed player disappears from top-N (page-enrichment-style
  test).
- **SDKs:** happy-path test per SDK against the test server.
