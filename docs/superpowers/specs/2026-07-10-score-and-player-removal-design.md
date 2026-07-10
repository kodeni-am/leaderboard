# Score & Player Removal — Design

**Date:** 2026-07-10
**Status:** Approved for implementation

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

| Op             | Meaning                                              | Fields used          |
|----------------|------------------------------------------------------|----------------------|
| "" / `submit`  | score submission (default; all existing log entries) | all (unchanged)      |
| `remove`       | remove one member's entry from one logical board     | App, Board, Member   |
| `deletePlayer` | remove member from all boards + users registry       | App, Member          |

- Absent/empty `op` decodes as a submit, so the log format is backward
  compatible; encode/decode stays plain JSON in the stream's `d` field.
- Tombstones carry an idempotency key like submits, so redelivery is safe.
- Score/Segments are unused on tombstones.

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

`recordToOps`/`recordsToOps` and both consumers (`Consumer`,
`GroupConsumer`) branch on `Op`:

- Submits batch into `SubmitBatch` as today.
- Tombstones call the shared removal apply function.
- A batch is **split at op boundaries** so log order is preserved — a remove
  that follows a submit of the same member is applied after it.

Rebuild-from-log (`Rebuild` → `Consumer.Drain`) therefore reproduces every
deletion at its position in history. A `remove` is not a ban: submits later
in the log (or new live submits) re-add the member, which is the intended
semantics.

### Engine fan-out

`engine.Remove` operates on one *physical* board; a submit fans out to
`windows × segments` physical boards. A new shared helper removes a member
from **all physical keys currently live** for a logical board:

- Derive current-window physical boards the same way the submit path does
  (`DerivePhysicalBoards`).
- Additionally scan for other live window keys of that board (per segment,
  `lb:{app:board:*}` pattern) so past windows that the reaper has not yet
  aged out are cleaned too.

`RedisEngine.Remove` already maintains the sorted set and the approx-rank
histogram correctly; `ShardedEngine.Remove` already routes to the member's
shard. This helper runs in both the API handler (immediate apply) and the
consumers (replay).

### Player deletion

For `deletePlayer`, the apply function:

1. Lists the app's boards from the tenancy store.
2. Runs the per-board removal (above) for each.
3. Calls a new `users.Delete(app, member)` that removes the registration and
   frees the nickname, using the same locking pattern as `users.Rename` to
   avoid racing a concurrent rename/register.

Deleting an absent user is a no-op, keeping replay idempotent.

**Re-register race:** if the player re-registers between tombstone append and
consumer apply, the consumer must not delete the fresh registration. The
consumer skips the registry deletion when the registration is **newer than
the tombstone's timestamp**. Registrations carry a created-at timestamp (add
one if not already present).

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
  decode as submits; consumers apply `remove` and `deletePlayer`; batch
  splitting preserves submit→remove ordering; redelivery is idempotent.
- **Rebuild:** submit → remove → rebuild-from-log → member absent (the core
  invariant this design exists for).
- **Re-register race:** a registration newer than the tombstone survives
  consumer replay.
- **API:** both auth paths (API key; session + CSRF); 404/204 semantics;
  log-append failure fails the request.
- **Dashboard:** removed player disappears from top-N (page-enrichment-style
  test).
- **SDKs:** happy-path test per SDK against the test server.
