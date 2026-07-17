# OpenLeaderboard

**Website / hosted instance: [openleaderboard.app](https://openleaderboard.app/)** — sign up, create an app, and get an API key (or self-host).

A fast, open-source, multi-tenant **leaderboard service** for game developers.
Built for scale — single boards into the 100M+ entry range, bursty score
writes — with **rank reads that don't slow down as boards grow** (rank is
intrinsic to the underlying Redis sorted set: O(log N)).

Apache-2.0. Self-host it or run it as a hosted multi-tenant API on AWS. No
billing/monetization layer — it's fully open source.

```
                    ┌─────────────────────────────────────────┐
   game client ───► │  Query/Read API        Ingestion API     │ ◄── game client
                    └──────────┬───────────────────┬───────────┘
                               │ read              │ submit (validate, idempotency, HMAC)
                               │              ┌─────▼────────────┐
                               │              │  durable log     │  Redis Streams / Kinesis
                               │              │  (source of      │
                               │              │   truth)         │
                               │              └─────┬────────────┘
                               │         fan-out    │ consumer
                          ┌────▼────────────────────▼───────────┐
                          │        Core Ranking Engine           │
                          │  sorted sets · ZADD GT · two-tier    │
                          │  rank · windows · segments · friends │
                          └────┬─────────────────────────────────┘
                          ElastiCache / Valkey (rebuildable cache)
```

## Why it's built this way

Decisions are grounded in researched, verified production practice (see
[`docs/superpowers/specs`](docs/superpowers/specs)):

- **Redis sorted sets** are the ranking primitive — `ZADD`/`ZINCRBY` are
  O(log N), `ZRANGE`/`ZRANK` are O(log N + M). Rank-read latency does **not**
  degrade at 100M members; the real limits are write throughput and memory.
- **Best-score-wins** uses `ZADD GT`/`LT` (atomic, no read-modify-write).
- **A durable log sits in front** of the ranking tier (Approach B). The log is
  the source of truth, so the Redis tier is a **rebuildable cache** — this
  absorbs write bursts, decouples the multi-board fan-out, and gives
  idempotency, replay, and rebuild for free.
- **Approximate deep-tail rank** uses O(1) bucket histograms (`HINCRBY`) — no
  Redis modules required, so it runs on stock ElastiCache/Valkey. Exact
  ordering is kept for top-N and "me ± neighbors".

## Features

- **Board types:** global all-time, time-windowed (daily/weekly/monthly/custom
  seasonal), segmented (region/platform/cohort), and friend/relative
  ("me ± neighbors", "rank among friends").
- **Score semantics:** higher- or lower-is-better; best/last/increment update
  policies; lexical or time-based (`firstToReach`) tie-breaking.
- **Write-behind ingestion** with idempotency and rebuild-from-log.
- **Multi-tenant:** apps with hashed API keys; per-app board definitions.
- **Window lifecycle:** current-window resolution + a reaper that ages out old
  windows from the cache.
- **Anti-cheat (optional, per-app):** opt-in HMAC-signed submissions with a
  replay window. Each app gets its own signing secret (derived from a server
  master key, never stored), so a public multi-tenant host can offer it
  per-tenant without sharing one global secret.
- **Moderation:** remove a member's entry from a board, or delete a player
  entirely (all boards + registered nickname) — from the dashboard, the API
  (`DELETE /v1/boards/{board}/scores/{member}`, `DELETE /v1/users/{id}`), or
  any SDK. Removals are durable tombstones in the ingest log, so they survive
  cache rebuilds. Note: moderation accepts plain API-key auth even on apps
  with `require_signing` — on those apps, treat the API key as a server-side
  secret (signing protects submits, not deletes), and note that segment names
  are enumerable by any key holder via `GET /v1/boards/{board}/segments`.
- **Reference Go SDK.**

## Quickstart (local, Docker)

No Go or Redis needed on your host — everything runs in containers.

```bash
docker compose up --build leaderboardd     # starts Redis + the server on :8080
curl localhost:8080/healthz                 # {"status":"ok"}
```

Get an API key, define a board, submit and query. **Easiest:** sign up in the
**dashboard** (`web/`) and create an app — it shows the API key once. Or via the
account API directly (signup → verify the emailed link → log in → create app):

```bash
BASE=localhost:8080

# 1. Create an account, verify the email (in dev, open Mailpit at :8025 for the
#    link), then log in to get a session cookie + CSRF token.
curl -s -X POST $BASE/auth/signup -d '{"email":"you@example.com","password":"hunter2hunter"}'
#    → open the verification link from Mailpit, then:
curl -s -c cj.txt -X POST $BASE/auth/login -d '{"email":"you@example.com","password":"hunter2hunter"}'
#    grab "csrf_token" from that response, then create an app (key shown once):
APP=$(curl -s -b cj.txt -H "X-CSRF-Token: <csrf>" -X POST $BASE/v1/apps -d '{"name":"DemoGame"}')
KEY=$(echo "$APP" | sed -E 's/.*"api_key":"([^"]+)".*/\1/')

# 2. Define a board (higher is better; all-time + daily windows)
curl -s -X POST $BASE/v1/boards -H "Authorization: Bearer $KEY" \
  -d '{"board":"high","sort_order":"desc","update_policy":"best","windows":[{"kind":"all"},{"kind":"daily"}]}'

# 3. Submit scores (write-behind; applied within ~50ms)
curl -s -X POST $BASE/v1/boards/high/scores -H "Authorization: Bearer $KEY" \
  -d '{"member":"bob","score":500,"segments":["all","region=eu"]}'

# 4. Query
curl -s "$BASE/v1/boards/high/top?n=10"                 -H "Authorization: Bearer $KEY"
curl -s "$BASE/v1/boards/high/rank?member=bob"          -H "Authorization: Bearer $KEY"
curl -s "$BASE/v1/boards/high/neighbors?member=bob&k=5" -H "Authorization: Bearer $KEY"
curl -s "$BASE/v1/boards/high/top?n=10&window=daily"           -H "Authorization: Bearer $KEY"
curl -s "$BASE/v1/boards/high/top?n=10&segment=region=eu"      -H "Authorization: Bearer $KEY"

# Optional player registry: register once, then submit with the minted id.
curl -s -X POST $BASE/v1/users -H "Authorization: Bearer $KEY" \
  -d '{"nickname":"Ninja"}'                    # -> {"user_id":"plr_...","nickname":"Ninja",...}
curl -s -X POST $BASE/v1/boards/high/scores -H "Authorization: Bearer $KEY" \
  -d '{"member":"plr_...","score":1500}'

# Or claim an existing anonymous member id in place ("bob" above): the
# nickname attaches to all of bob's existing rows — no resubmit, no delete.
curl -s -X POST $BASE/v1/users -H "Authorization: Bearer $KEY" \
  -d '{"nickname":"Bobby","member":"bob"}'     # -> {"user_id":"bob","nickname":"Bobby",...}
```

## API

| Method & path | Purpose |
|---|---|
| `GET /healthz` | Liveness |
| `POST /auth/signup` · `/auth/login` · `/auth/logout` | Account auth (session cookie) |
| `GET /auth/verify` · `POST /auth/forgot` · `/auth/reset` | Email verification & password reset |
| `POST /v1/apps` · `GET /v1/apps` | Create/list apps (session-authed, owner-scoped) → key shown once |
| `GET /v1/apps/{id}/stats` | App counters — `{"players": N}` registered (session-authed, owner-scoped) |
| `POST /v1/boards` | Define a board |
| `GET /v1/boards` | List boards |
| `POST /v1/boards/{board}/scores` | Submit a score (write-behind) |
| `GET /v1/boards/{board}/rank?member=` | A member's rank |
| `GET /v1/boards/{board}/top?n=` | Top N |
| `GET /v1/boards/{board}/page?offset=&limit=` | Paginate |
| `GET /v1/boards/{board}/neighbors?member=&k=` | Me ± k |
| `POST /v1/boards/{board}/friends` | Rank an explicit member list |
| `GET /v1/boards/{board}/segments` | List the segment names currently live on a board |
| `GET /v1/boards/{board}/count` | How many members are on the board (honors `segment=`/`window=`) |
| `POST /v1/users` | Register a player (server-minted id + nickname, unique per app); `member` claims an existing member id in place |
| `GET /v1/users/{id}` · `GET /v1/users?nickname=` | Fetch / resolve a player |
| `PATCH /v1/users/{id}` | Rename a player (id and board data unaffected) |

All query endpoints accept `segment=` and `window=` (a literal id like
`d=2026-06-13`, or a cadence keyword `daily`/`weekly`/`monthly`).

Read entries include a `nickname` field for members registered via
`/v1/users`; raw (unregistered) member strings keep working and simply omit
it. Nicknames are unique per app, case-insensitively; renames are O(1) and
never touch board data.

Registration can also **claim an existing anonymous member id** (`{"nickname":
..., "member": ...}`): the id is registered as-is, so the nickname attaches to
all of that member's existing board rows — the one-call upgrade path for games
that submit under per-install anonymous ids. Claimed ids can't use the
reserved server-minted `plr_` prefix; conflicts return distinct codes
(`member_taken` vs `nickname_taken`). Trust caveat: the API key is the only
data-plane credential, so any client holding it can claim a nickname for any
raw member id — the same trust level as unsigned score submits. If that
matters for your app, enable HMAC signing and proxy registration through your
backend.

**Two auth planes** on the data plane (`/v1/boards/*` and `/v1/users*`): game clients use `Authorization: Bearer
<api-key>` (or `X-API-Key`); the dashboard uses its session cookie plus an
`X-App-Id` header for an app the logged-in user owns (CSRF required on mutations).

## Repository layout

| Package | Sub-project | What |
|---|---|---|
| `pkg/engine` | SP1 | Core ranking engine over Redis sorted sets |
| `pkg/ingest` | SP2 | Durable log (mem/Redis-Streams) + fan-out consumer |
| `pkg/api` | SP3 | HTTP JSON API |
| `pkg/window` | SP4 | Window resolution + reaper |
| `pkg/tenancy` | SP5 | Apps, API keys, board definitions |
| `pkg/trust` | SP7 | HMAC submission verification |
| `pkg/sdk` | SP8 | Reference Go client |
| `sdk/unity` | SP8 | Unity/C# client SDK (UPM package, async/await) |
| `sdk/typescript` | SP8 | TypeScript client SDK (browser + Node 18+) — [![npm](https://img.shields.io/npm/v/@openleaderboard/sdk.svg?label=npm&color=c6f135)](https://www.npmjs.com/package/@openleaderboard/sdk) |
| `pkg/accounts` | SP9 | User accounts, sessions, email verification/reset |
| `pkg/email` | SP9 | Transactional email (console + SMTP) |
| `web` | SP10 | React+Vite landing page + dashboard |
| `cmd/leaderboardd` | — | The server binary |
| `deploy/terraform` | SP6 | AWS reference architecture (scaffold) |

## Configuration (env)

| Var | Default | Notes |
|---|---|---|
| `REDIS_ADDR` | `localhost:6379` | Redis/ElastiCache endpoint. Comma-separated seeds → [Redis Cluster](deploy/README-cluster.md) |
| `LISTEN_ADDR` | `:8080` | HTTP listen address |
| `LB_LOG_BACKEND` | `redis` | `redis` (Streams) or `mem` |
| `LB_STREAM` | `lb:ingest` | Redis stream name |
| `PUBLIC_URL` | `http://localhost:8080` | Origin used in account email links |
| `SECURE_COOKIES` | `false` | Set `true` behind HTTPS (Secure cookie flag) |
| `CORS_ORIGINS` | `*` | Browser CORS: `*` (any origin, API-key only) or a comma-separated origin allowlist (reflects + allows credentials). Empty disables CORS |
| `SMTP_HOST` / `SMTP_PORT` / `SMTP_USER` / `SMTP_PASS` / `SMTP_FROM` | _(unset)_ | Email transport; unset → console sender (links logged) |
| `SIGNING_SECRET` | _(unset)_ | Master key for per-app signed submissions; per-app secrets are derived from it. Unset → apps can't enable signing |
| `LB_REAPER_RETAIN` | _(unset)_ | e.g. `168h` to enable the window reaper |
| `INGEST_PARTITIONS` | `16` | Stream partitions (set once; changing it later rehashes routing) |
| `WORKER_INDEX` | `0` | This worker's index for static partition ownership |
| `WORKER_COUNT` | `1` | Total workers; each owns partitions where `p % count == index` |
| `BOARD_SHARDS` | `1` | >1 enables [intra-board sharding](#sharding-one-board-across-nodes) (one board split across N sorted sets; rank reads become approximate) |

### Scaling consumers

The ingest log is **partitioned by `(app, board, member)`** across `INGEST_PARTITIONS`
Redis streams. The live consumer uses **Redis Streams consumer groups**
(`XREADGROUP`/`XACK`), so:

- **Offsets are durable** — a restart resumes from un-acked entries instead of
  replaying the whole log (no double-counting on `increment` boards).
- **Scale horizontally** — run N copies with `WORKER_COUNT=N` and distinct
  `WORKER_INDEX` (0..N-1); each owns a disjoint set of partitions. Per-member
  ordering is preserved because a member's events always share one partition.
- **Crash recovery** — a dead worker's un-acked entries are reclaimed via
  `XAUTOCLAIM`.

Delivery is at-least-once with idempotent apply (each entry is marked applied
after processing), so `best`/`last` are effectively exactly-once. The one
residual: an `increment` board can over-count a single entry if a worker crashes
in the narrow window between applying a batch and marking it — rare, and
bounded to in-flight entries.

### Scaling across nodes (Redis Cluster)

When one node can't hold all your boards (or push their aggregate throughput),
point `REDIS_ADDR` at comma-separated cluster seeds. Boards spread across nodes
via per-board hash tags, and the consumer reads each partition stream on its own
slot, so the same binary runs on single-node and cluster unchanged. Details,
caveats, and a throwaway 6-node test cluster: **[deploy/README-cluster.md](deploy/README-cluster.md)**.

### Sharding one board across nodes

Redis Cluster spreads *different* boards across nodes, but a single board's
sorted set still lives on one node. When one board outgrows a node, set
`BOARD_SHARDS=N` to split it into N sorted sets (`board#s0…#s{N-1}`), each on its
own hash slot. Members are assigned to a shard by a stable hash, so a member
always lands on the same shard. What this costs and preserves:

| Operation | Sharded behavior |
|---|---|
| submit / remove / count | exact — routed to the member's shard (count sums shards) |
| top-N / page | **exact** — k-way merge of each shard's top range |
| friends | exact — scores gathered across shards, ranked within the set |
| rank | **approximate** (`exact:false`) — summed per-shard histograms; requires `approx_rank` on the board |
| me ± neighbors | exact members/order in the window; rank numbers approximate |

Because rank becomes approximate, sharded boards should be created with
`approx_rank: true`. A board big enough to shard is past the point where an exact
global rank scan is cheap, so this is the intended trade — top-N, pages, and
neighbor lists stay exact.

**Performance characteristic.** Sharding scales writes and memory across nodes
(write throughput is unchanged from single-set) and keeps top-N/page/neighbors
exact. Approximate rank, though, sums all shards' histograms per call, so a rank
read costs roughly `O(shards × approx_buckets)` (one pipelined round trip,
independent of member count). Fewer buckets → faster rank reads but coarser
resolution; for rank-read-heavy workloads, cache the merged histogram. Measure
your shard count and bucket size with `go run ./cmd/loadtest -mode engine -shards N`.

## Testing

```bash
make test        # full suite against real Redis in Docker
make test-engine # engine package, verbose
make cover       # with coverage
```

Tests run against a **real Redis** (sorted-set semantics like `ZADD GT` must be
faithful), including a property test that checks engine ranks against a
brute-force reference.

## Benchmarking

A load-test harness (`cmd/loadtest`) validates the core bet — that rank-read
latency stays flat as a board grows — and finds the point where a single sorted
set stops scaling.

```bash
make loadtest                              # engine mode against the compose Redis
# intra-board sharding (approximate rank tier):
go run ./cmd/loadtest -mode engine -shards 8 -sizes 1000000 -dur 5s
# or, against a running server (full stack):
go run ./cmd/loadtest -mode http -url http://localhost:8080 \
  -api-key lb_yourkey -size 100000 -readers 16 -writers 16 -dur 5s
```

**Engine mode** seeds a board to each size and measures `GetRank` latency;
**HTTP mode** drives a live server (API + ingest + consumer).

Indicative local result (single Docker Redis on a laptop — *not* a production
ElastiCache benchmark), showing read latency is essentially size-independent:

| board size | reads/s | p50 | p90 | p99 |
|---|---|---|---|---|
| 1,000 | 92,883 | 77µs | 140µs | 242µs |
| 10,000 | 92,451 | 78µs | 138µs | 239µs |
| 100,000 | 91,830 | 79µs | 137µs | 239µs |
| 1,000,000 | 92,997 | 80µs | 130µs | 222µs |

p99 barely moves from 1k to 1M members — the O(log N) sorted-set property holds.
Write throughput on the same box was ~91k best-wins submits/s into the 1M board.

## Production on a VPS (no AWS)

`deploy/docker-compose.prod.yml` runs the full stack on any Docker host:
persistent Redis (AOF), `leaderboardd`, a **Caddy** reverse proxy with automatic
HTTPS, and **Prometheus**.

```bash
cp deploy/.env.example deploy/.env     # set DOMAIN, PUBLIC_URL, SMTP_*, ...
docker compose -f deploy/docker-compose.prod.yml --env-file deploy/.env up -d --build
```

- **TLS** — Caddy obtains a real certificate for `$DOMAIN` automatically (use
  `DOMAIN=localhost` for a local trial with Caddy's internal CA).
- **Durability** — Redis runs with `appendonly yes` + `noeviction` and a named
  volume, so the Streams log (the source of truth on a single VPS) survives
  restarts. Point `REDIS_ADDR` at a managed/HA Redis to separate it from the
  app box.
- **Metrics** — `leaderboardd` exposes Prometheus metrics at `/metrics`
  (`lb_http_requests_total`, `lb_http_request_duration_seconds`,
  `lb_submits_total{result=...}`, `lb_consumer_records_applied_total`). Caddy
  blocks `/metrics` publicly; Prometheus scrapes it over the internal network
  and is bound to `127.0.0.1:9090`.
- **Dashboards & alerts** — Grafana (`127.0.0.1:3000`) is auto-provisioned with
  the Prometheus datasource and an **OpenLeaderboard** dashboard (request rate,
  read-latency p50/p95/p99, error ratio, submit outcomes, ingest throughput).
  Prometheus loads `deploy/alerts.yml` and routes to Alertmanager
  (`127.0.0.1:9093`). Shipped alerts: `LeaderboardTargetDown`,
  `HighHTTPErrorRate`, `HighReadLatencyP99`, `ConsumerStalled`,
  `HighSubmitRejectionRate`. Point the Alertmanager receiver in
  `deploy/alertmanager.yml` at your Slack/PagerDuty/webhook to get notified.
  Reach Grafana over an SSH tunnel: `ssh -L 3000:localhost:3000 user@host`.

## Deploy on Runnable (or any compose host)

The image serves the **dashboard SPA and the API on one origin** (`WEB_DIR=/web`,
baked in by the multi-stage `deploy/Dockerfile`), so a single web service is all
you need. `deploy/runnable-compose.yml` defines that service plus a persistent
Redis.

In Runnable: connect the repo, enable **compose mode** with
`composeFile: deploy/runnable-compose.yml` and **`composeService: leaderboardd`**
(port 8080). Runnable provides the domain, TLS, and reverse proxy. Set env:

- `PUBLIC_URL` — the app's public URL (used in account email links)
- `SMTP_HOST` / `SMTP_PORT` / `SMTP_USER` / `SMTP_PASS` / `SMTP_FROM` — for
  verification / reset email (required for signups)
- `SIGNING_SECRET` — optional master key enabling per-app signed submissions
  (anti-cheat). Tenants opt in per app from the dashboard and get their own
  derived secret; keep this master key private. Safe to set on a shared host.
- `CORS_ORIGINS` — defaults to `*` so browser games on any domain can call the
  API with their key. Set a comma-separated allowlist to restrict it.

`SECURE_COOKIES` is already `true` and `REDIS_ADDR=redis:6379` is wired. Pushes
to the repo auto-deploy.

## Deploying to AWS

`deploy/terraform` provisions ElastiCache (ranking tier), Kinesis (durable log),
DynamoDB, and an ECS Fargate service behind an ALB. It `terraform validate`s
clean but is a **reviewed scaffold** — review IAM, networking, secrets handling,
and HA before production use, and wire a `KinesisLog` backend (the `Log`
interface already supports it).

## Known limitations / roadmap

Honest about what v1 is and isn't:

- **Intra-board sharding** for a single board exceeding one node ships behind
  `BOARD_SHARDS` (see [Sharding one board across nodes](#sharding-one-board-across-nodes)).
  Top-N, pages, and neighbor windows stay exact via k-way merge; global rank
  becomes an `exact:false` histogram estimate. The remaining tuning is empirical:
  the breakpoint where a single set must shard, and the optimal shard count,
  should be measured per workload with `cmd/loadtest`.
- **KinesisLog** is provisioned by IaC but not yet implemented in code (Redis
  Streams + in-memory logs ship today; the `Log` interface is the seam).
- **Timezone-aware windows** aren't built — daily/weekly/monthly buckets reset at
  UTC midnight (`WindowID` keys off the event time's UTC date). For a non-UTC
  reset, slice by region with segments or rotate a `custom` window id at local
  midnight. Note: don't pre-shift the submitted `time` to fake a local day — it
  also encodes `firstToReach` order, so shifting corrupts tie-breaks. A per-board
  IANA timezone is a candidate once there's demand (needs tzdata in the image).
- **Multi-region active-active** is out of scope for v1.
- **Statistical anomaly detection** beyond HMAC verification is a documented
  log-consumer seam.

## License

Apache-2.0.
