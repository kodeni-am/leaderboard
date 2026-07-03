# Users & Nicknames — Design Spec

**Date:** 2026-07-03
**Status:** Draft — pending user review

## Goal
Introduce a **user** entity on the data plane: a server-minted unique ID per
player plus a friendly **nickname that is unique per app**. Leaderboard reads
return the nickname alongside the raw member so games can render human names.

## Decisions (made during brainstorming)
1. **Server-minted user IDs** — games register a user and receive `plr_<hex>`
   (`plr_` not `usr_`: dashboard account IDs already use the `usr_` prefix in
   `pkg/accounts`, and the two identity types must stay distinguishable);
   they then submit scores with that ID as the `member`.
2. **Lenient compatibility** — unregistered, arbitrary `member` strings keep
   working exactly as today. Registered users additionally get a nickname in
   read results. No breaking changes; submit hot path gains zero Redis calls.
3. **Nickname collisions → 409** — uniqueness is enforced per app,
   **case-insensitively** (`Ninja` blocks `ninja`). The game prompts the player
   to pick another name.
4. **API-layer enrichment** — nicknames live in their own Redis keys; the
   ranking engine stays tenancy-agnostic and untouched. Read handlers enrich
   entries with one pipelined `HMGET` after the engine call.

## Data model
```
User {
  ID        string  // "plr_" + 12 hex, minted like tenancy's "app_" IDs
  AppID     string
  Nickname  string  // display form as entered
  CreatedAt time.Time
  UpdatedAt time.Time
}
```
**Nickname rules:** trimmed; 1–32 chars after trim; no control characters;
Unicode allowed. Uniqueness key is `strings.ToLower(nickname)`.

## Storage — new `pkg/users`
Mirrors `pkg/accounts` / `pkg/tenancy`: `Store` interface + `memstore` +
`redisstore` + shared conformance tests.

Keys carry a `{app}` hash tag so all of an app's user keys share a cluster
slot, allowing atomic Lua scripts:
```
plr:{<app>}:user:<id>   JSON user record
plr:{<app>}:names       HASH id -> nickname          (batch enrichment)
plr:{<app>}:nick        HASH lower(nick) -> id       (uniqueness claim)
```
- **Create** (Lua, atomic): `HSETNX` on the nick hash — if the claim fails,
  return conflict (→ 409); otherwise write the record and the names entry.
- **Rename** (Lua, atomic): claim new name, release old, update record +
  names entry. O(1) regardless of how many boards the user appears on.
- **Case-only rename** (`Ninja` → `NINJA`): same lowercase key → allowed;
  updates the stored display form.

## API (data-plane auth: API key or session + `X-App-Id`, same as boards)
| Method | Path | Behavior |
|---|---|---|
| POST | `/v1/users` | body `{"nickname": "..."}` → **201** `{user_id, nickname}`; **409** `nickname_taken`; **400** invalid nickname |
| GET | `/v1/users/{id}` | user record; **404** unknown |
| PATCH | `/v1/users/{id}` | body `{"nickname": "..."}` → rename; **409**/**400** as above |
| GET | `/v1/users?nickname=X` | reverse lookup (case-insensitive); **404** if unclaimed |

Error responses follow the existing API error shape with stable error codes
(`nickname_taken`, `invalid_nickname`, `user_not_found`).

## Read enrichment
- `engine.RankEntry` gains `Nickname string \`json:"nickname,omitempty"\``.
  The engine never populates it (stays tenancy-agnostic).
- An API-layer helper takes any `[]RankEntry`, collects the member strings,
  issues one `HMGET plr:{app}:names m1 m2 ...`, and fills matches.
- Applied to `rank`, `top`, `page`, `neighbors`, `friends`.
- Unregistered members simply omit the `nickname` key in JSON.
- Cost: one extra pipelined Redis round-trip per read request (sub-ms), zero
  cost on submit.

## Submit path
Unchanged. `member` remains an opaque string; registered games pass the
`plr_...` ID. HMAC signing is unaffected — it signs the member string, which
is now a stable ID, so **renames never invalidate signatures**.

## SDKs & dashboard
- **Go SDK** (`pkg/sdk`), **TypeScript SDK** (`sdk/typescript`), **Unity SDK**
  (`sdk/unity`): add `RegisterUser(nickname)`, `GetUser(id)`,
  `RenameUser(id, nickname)`; add `nickname` to entry models; surface the 409
  as a typed "nickname taken" error so games can prompt for another name.
- **Web dashboard** (`web/`): leaderboard viewer shows nickname when present
  (falls back to raw member); test-submit panel gets a small "register user"
  helper so testers can create named users. A full user-management tab is
  **out of scope** (future work).

## Out of scope (future work)
- Deleting users / releasing nicknames.
- Per-app policy flags (e.g. require-registered-users), profanity filtering,
  reserved names.
- Dashboard user-management tab.

## Testing
- **Store conformance** (mem + Redis): create/get/lookup; collision → conflict;
  case-insensitive collision; rename atomicity incl. concurrent claim race;
  case-only rename.
- **API handler tests**: 201/400/404/409 paths; enrichment present on all five
  read endpoints; unregistered members unaffected.
- **SDK tests**: register → submit → top returns nickname round-trip, per
  existing SDK test patterns.
- All via `make test` / docker compose (Docker-only toolchain).
