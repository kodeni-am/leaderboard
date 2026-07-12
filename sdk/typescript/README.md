# OpenLeaderboard — TypeScript SDK

[![npm version](https://img.shields.io/npm/v/@openleaderboard/sdk.svg?color=c6f135&label=npm)](https://www.npmjs.com/package/@openleaderboard/sdk)
[![npm downloads](https://img.shields.io/npm/dm/@openleaderboard/sdk.svg)](https://www.npmjs.com/package/@openleaderboard/sdk)
[![license](https://img.shields.io/npm/l/@openleaderboard/sdk.svg)](../../LICENSE)

A dependency-free client for the [OpenLeaderboard](../../README.md) API. Works in
**browsers** and **Node 18+** (uses the global Fetch API and Web Crypto).

## Install

```bash
npm install @openleaderboard/sdk
```

## Quickstart

```ts
import { LeaderboardClient, NotFoundError } from "@openleaderboard/sdk";

const lb = new LeaderboardClient("https://lb.example.com", "lb_your_api_key");

// Submit (write-behind: durably logged, ranked shortly after).
await lb.submitScore("high", playerId, 1500);

// Read back.
const me = await lb.getRank("high", playerId);            // throws NotFoundError if absent
const top = await lb.getTop("high", 10);
const near = await lb.getNeighbors("high", playerId, 5);  // me ± 5
const friends = await lb.getFriends("high", ["alice", "bob"]);

// Segmented / windowed reads (window: literal id or "daily"/"weekly"/"monthly").
await lb.getTop("high", 10, { segment: "region=eu", window: "daily" });
```

Errors: `NotFoundError` (404) and `LeaderboardError` (other non-2xx, with `.status`).

### Player registry (nicknames)

```ts
// Mint a player id and claim a nickname (unique per app, case-insensitive).
const u = await lb.registerUser("Ninja");            // throws NicknameTakenError on conflict
await lb.submitScore("high", u.user_id, 1500);       // reads now include nickname

// Or claim an EXISTING anonymous member id in place: user_id echoes it and
// the nickname attaches to all its existing board rows — no resubmit, no
// delete. Throws MemberTakenError if that id is already registered.
await lb.registerUser("Ninja", { member: anonymousId });

await lb.renameUser(u.user_id, "Shadow");            // id and board data unaffected
```

Trust caveat for claims: the API key is the only data-plane credential, so any
client holding it can claim a nickname for any raw member id — the same trust
level as unsigned score submits. Treat claims as untrusted input unless you
sign submissions and proxy registration through your backend.

### One-time board setup

```ts
await lb.createBoard("laptimes", { sortOrder: "asc", updatePolicy: "best" });
await lb.createBoard("weekly", { windows: [{ kind: "all" }, { kind: "weekly" }] });
```

## Node < 18

Pass a fetch implementation:

```ts
import fetch from "node-fetch";
new LeaderboardClient(url, key, { fetch });
```

## Signed submissions (server-side only)

`signSubmission` and the client's `signingSecret` option produce HMAC signatures
matching the server's `SIGNING_SECRET`. **Never put the secret in browser/client
code** — anyone can read it. Sign from a trusted backend instead. Integer scores
sign identically to the Go server (cross-validated against the server and
`openssl`).

```ts
const lb = new LeaderboardClient(url, key, { appId, signingSecret }); // backend only
```

## Develop

```bash
npm run typecheck
npm run build
npm run test:unit        # offline request/error-shape checks (mock fetch)
npm run test:hmac        # offline HMAC cross-check
npm run test:e2e         # against a running server (LB_API_KEY env)
```

## Releasing

`package.json` is authoritative. Bump `version` in your PR; when it merges to
`main`, CI (`.github/workflows/release-sdk.yml`) publishes exactly that version
to npm. Changes that don't bump the version are a no-op (already published →
skipped). npm versions are immutable, so each release needs a new version.
