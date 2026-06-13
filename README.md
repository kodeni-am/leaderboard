# OpenLeaderboard

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
- **Anti-cheat (optional):** HMAC-signed submissions with a replay window.
- **Reference Go SDK.**

## Quickstart (local, Docker)

No Go or Redis needed on your host — everything runs in containers.

```bash
docker compose up --build leaderboardd     # starts Redis + the server on :8080
curl localhost:8080/healthz                 # {"status":"ok"}
```

Create a tenant, define a board, submit and query:

```bash
BASE=localhost:8080

# 1. Create an app (admin token from docker-compose.yml)
APP=$(curl -s -X POST $BASE/v1/apps -H "X-Admin-Token: dev-admin-token" -d '{"name":"DemoGame"}')
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
```

## API

| Method & path | Purpose |
|---|---|
| `GET /healthz` | Liveness |
| `POST /v1/apps` | Create tenant (requires `X-Admin-Token`) → returns one-time API key |
| `POST /v1/boards` | Define a board |
| `GET /v1/boards` | List boards |
| `POST /v1/boards/{board}/scores` | Submit a score (write-behind) |
| `GET /v1/boards/{board}/rank?member=` | A member's rank |
| `GET /v1/boards/{board}/top?n=` | Top N |
| `GET /v1/boards/{board}/page?offset=&limit=` | Paginate |
| `GET /v1/boards/{board}/neighbors?member=&k=` | Me ± k |
| `POST /v1/boards/{board}/friends` | Rank an explicit member list |

All query endpoints accept `segment=` and `window=` (a literal id like
`d=2026-06-13`, or a cadence keyword `daily`/`weekly`/`monthly`). Auth via
`Authorization: Bearer <key>` or `X-API-Key`.

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
| `cmd/leaderboardd` | — | The server binary |
| `deploy/terraform` | SP6 | AWS reference architecture (scaffold) |

## Configuration (env)

| Var | Default | Notes |
|---|---|---|
| `REDIS_ADDR` | `localhost:6379` | Redis/ElastiCache endpoint |
| `LISTEN_ADDR` | `:8080` | HTTP listen address |
| `LB_LOG_BACKEND` | `redis` | `redis` (Streams) or `mem` |
| `LB_STREAM` | `lb:ingest` | Redis stream name |
| `ADMIN_TOKEN` | _(unset)_ | Required to create apps |
| `SIGNING_SECRET` | _(unset)_ | Enables HMAC submission verification |
| `LB_REAPER_RETAIN` | _(unset)_ | e.g. `168h` to enable the window reaper |

## Testing

```bash
make test        # full suite against real Redis in Docker
make test-engine # engine package, verbose
make cover       # with coverage
```

Tests run against a **real Redis** (sorted-set semantics like `ZADD GT` must be
faithful), including a property test that checks engine ranks against a
brute-force reference.

## Deploying to AWS

`deploy/terraform` provisions ElastiCache (ranking tier), Kinesis (durable log),
DynamoDB, and an ECS Fargate service behind an ALB. It `terraform validate`s
clean but is a **reviewed scaffold** — review IAM, networking, secrets handling,
and HA before production use, and wire a `KinesisLog` backend (the `Log`
interface already supports it).

## Known limitations / roadmap

Honest about what v1 is and isn't:

- **Intra-board sharding** for a single board exceeding one node is designed
  (the `Histogram` approximate-rank tier exists and is tested) but the
  multi-node orchestration is a benchmarked follow-on — the breakpoint where a
  single sorted set must shard should be measured first.
- **KinesisLog** is provisioned by IaC but not yet implemented in code (Redis
  Streams + in-memory logs ship today; the `Log` interface is the seam).
- **Multi-region active-active** is out of scope for v1.
- **Statistical anomaly detection** beyond HMAC verification is a documented
  log-consumer seam.

## License

Apache-2.0.
