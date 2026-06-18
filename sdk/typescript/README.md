# OpenLeaderboard — TypeScript SDK

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
npm run test:hmac        # offline HMAC cross-check
npm run test:e2e         # against a running server (LB_API_KEY env)
```
