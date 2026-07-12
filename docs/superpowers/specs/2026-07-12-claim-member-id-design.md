# Claim an existing member id at registration — design

**Date:** 2026-07-12 · **Extends:** [2026-07-03-users-and-nicknames-design.md](2026-07-03-users-and-nicknames-design.md)

## Goal

Let a game turn an anonymous leaderboard member into a registered player **in
place**: same member id, nickname attaches to all existing board rows. One
call replaces the old register → resubmit bests → delete-old-member dance,
which lost past daily-window rows.

Motivating consumer: SWELL submits scores under a per-install anonymous id
(`surfer-xxxxxxxx`). When the player picks a nickname, registration previously
minted a new `plr_` id, forcing a lossy migration.

## Why this is cheap

The read path already supports it: `enrichEntries` (pkg/api/server.go)
decorates entries via a plain `HMGET` of the names hash keyed by the **raw
member id** — nothing requires the `plr_` prefix. `DELETE /v1/users/{id}` and
`PATCH /v1/users/{id}` also already operate on raw ids. Only registration
needed to learn about caller-supplied ids.

## Store contract (`pkg/users`)

`Create(ctx, appID, nickname, member)` — `member == ""` keeps minting
`"plr_" + hex`; non-empty claims that id. Both `RedisStore` (atomic Lua) and
`MemStore`:

- **`ErrMemberTaken`** (→ HTTP 409 `member_taken`, distinct from
  `nickname_taken`) if the id is already a registered player.
- Nickname uniqueness semantics unchanged (lowercased claim, display form
  stored). Member-taken is checked **before** the nickname claim, so a losing
  claim never consumes the nickname.
- **`ErrInvalidMember`** (→ HTTP 400 `invalid_member`): id is trimmed, must be
  1–64 runes, no control/format characters, and **must not start with `plr_`**
  — that namespace is reserved for server-minted ids so a client can never
  occupy it.

Rename/delete need no changes: they already treat the id as opaque; delete
releases the nickname and the raw id becomes claimable again.

## API

`POST /v1/users` body gains `member` (optional). Response shape unchanged —
`user_id` echoes the claimed id. Conflict codes are distinct so clients can
tell "pick another name" from "this member is already claimed".

**Trust model:** the data plane is API-key authenticated, so any client
holding the key can claim a nickname for ANY raw member id (impersonation of
anonymous rows). This is the same trust level as unsigned score submits. When
an app opts into HMAC submit enforcement (`RequireSigning`, verified in
`handleSubmit`), this endpoint must join it — tracked as a TODO(trust) on
`handleCreateUser`.

## SDKs

- **TypeScript** (`sdk/typescript`, 0.6.0): `registerUser(nickname, { member })`;
  409 splits into `MemberTakenError` vs `NicknameTakenError`.
- **Go** (`pkg/sdk`): `RegisterUser(ctx, nickname, RegisterUserOpts{Member: ...})`
  (variadic opts, backward compatible); adds `ErrMemberTaken`.

## Testing

- Store conformance (mem + Redis): claim with explicit id; duplicate id →
  `ErrMemberTaken`; `plr_`-prefixed / invalid ids rejected without consuming
  the nickname; nickname-taken still works with an explicit id; rename/delete
  on a claimed raw id (delete releases the nickname, id re-claimable);
  concurrent claimants of one member id — exactly one winner, no leaked
  nickname claims.
- API: 201 with `user_id == member`; distinct 409 codes; end-to-end submit as
  raw member → claim → `GET top` shows the nickname on the existing row
  (proves `enrichEntries` needed no change).
- SDKs: Go claim flow against a live server; TS offline unit test asserts the
  request body includes `member` only when passed and the 409 split.
