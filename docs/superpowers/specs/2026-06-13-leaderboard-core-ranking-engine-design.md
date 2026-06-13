# OpenLeaderboard — Design Spec

**Date:** 2026-06-13
**Status:** Approved (brainstorming) — implementation in progress
**Scope of this document:** Overall system decomposition + detailed design of **SP1 (Core Ranking Engine)**, plus the integration contracts for SP2/SP3/SP4/SP5 that the goal-driven implementation will deliver.

---

## 1. Product

An **open-source** (Apache-2.0), high-performance leaderboard service for game developers. Hosted multi-tenant on AWS; also fully self-hostable. **No commercial/billing layer.**

**Core bet:** _speed & scale_ — "the fastest leaderboard API." Read-rank latency must not degrade with board size.

**Target scale (design envelope):** single boards up to 100M+ entries; bursty score writes of 50k+/sec; four board types: global all-time, time-windowed (daily/weekly/monthly/seasonal), segmented (region/platform/cohort), friend/relative ("me ± neighbors").

**Rank accuracy contract:** exact ordering for top-N and "me ± neighbors"; approximate absolute rank acceptable for the deep tail of very large (sharded) boards.

## 2. Research-grounded decisions (verified findings)

- **Sorted sets are the ranking primitive.** `ZADD`/`ZINCRBY` O(log N); `ZRANGE`/`ZRANK` O(log N + M). Read latency does **not** degrade at 100M members — limits are write throughput and memory, not rank reads.
- **Best-score-wins = `ZADD GT`/`LT`** (Redis 6.2+) — atomic, no read-modify-write.
- **Multi-dimensional boards cause write amplification** (one score → N physical sets). This is the dominant cost driver; we make it explicit and caller-controlled.
- **Durability fork:** ElastiCache (async repl, can lose writes on failover) vs MemoryDB (synchronous Multi-AZ log, ~2× cost). We choose **Approach B**: a durable ingest **log** is the source of truth; the Redis ranking tier is a **rebuildable cache** → ElastiCache is sufficient and cheaper.
- **Approximate tail = bucketed histograms via `HINCRBY` (O(1))**, stored in a Redis hash — portable, **no Redis module required** (ElastiCache lacks the t-digest module). t-digest is an optional in-process refinement.
- **Reference architecture** (Nakama, 2M CCU): in-memory ranking tier over a durable store; EKS/ALB/Aurora. Validates the pattern.
- **Do not assert** a fixed per-entry byte size, single-instance write ceiling, or "sub-millisecond" latency — these were refuted in research and must be established by our own benchmarks.

## 3. System decomposition & build order

| # | Sub-project | Role |
|---|---|---|
| **SP1** | Core Ranking Engine (Go library) | Board model, score semantics, exact tier, approx-tier seam. **This spec.** |
| SP2 | Ingestion write path | Submit → validate/idempotency → durable log → fan-out consumer → engine; rebuild-from-log. |
| SP3 | Query/Read API | HTTP/JSON: get-rank, top-N, me±neighbors, page, friend-rank. |
| SP4 | Window lifecycle | Window-id derivation, rollover/reset, TTL, sealing. |
| SP5 | Multi-tenancy | App registration, API keys, quota/rate hooks. |
| SP6 | AWS IaC | Terraform: ECS/Fargate, ElastiCache, Kinesis, DynamoDB, ALB, observability. |
| SP7 | Anti-cheat | HMAC score signing verification hook; anomaly consumer (off the log). |
| SP8 | Client SDKs | Go SDK (reference); JS/Unity later. |

**Build order:** SP1 → SP2 → SP3 → SP5/SP4 (functional, locally runnable & tested with real Redis) → SP6 IaC skeleton → SP7 hook → SP8 Go SDK.

The durable log and Kinesis are abstracted behind a `Log` interface so the whole system runs locally (in-memory/Redis-Streams log) and on AWS (Kinesis) without code change.

## 4. SP1 — Core Ranking Engine (detailed)

### 4.1 Boundary
Pure Go library (`pkg/engine`). Talks only to a Redis-compatible server via `go-redis/v9`. No HTTP, no AWS, no log. Fully testable against a local/containerized real Redis.

### 4.2 Board model & addressing
- **Logical board config:** `{AppID, BoardID, SortOrder, UpdatePolicy, TieBreak, Window, Segments}`.
- **Physical board** = one sorted set, keyed with a Redis Cluster **hash tag** so a board's keys co-locate in one slot:
  - `lb:{<app>:<board>:<segment>:<window>}:z`   — sorted set (exact tier)
  - `lb:{<app>:<board>:<segment>:<window>}:h`   — bucket histogram (approx tier; created lazily)
  - `lb:{<app>:<board>:<segment>:<window>}:meta` — board config/version
- `segment`: caller-supplied opaque string, default `all`. **Caller owns cardinality**; we document/meter the fan-out cost.
- `window`: `all` for global; else derived bucket id (`d=YYYY-MM-DD`, `w=YYYY-Www`, `m=YYYY-MM`, `s=<seasonId>`).
- **Friend/relative = query capability, not storage.** No physical friend sets. `FriendRank` reads a member-id list via `ZMSCORE` + orders them; `NeighborRange` uses `ZRANK(me)` + range. Zero extra keys.
- Pure function `DerivePhysicalBoards(cfg, event) → []BoardKey` (used by SP2 fan-out).

### 4.3 Score semantics (per-board, explicit)
- **SortOrder:** `desc` (higher wins, default) | `asc` (lower wins).
- **UpdatePolicy:** `best` (`ZADD GT`/`LT`) | `last` (plain `ZADD`) | `increment` (`ZINCRBY`).
- **TieBreak:** `lexical` (default — Redis breaks score ties by member id; deterministic, documented) | `firstToReach` (composite encoding packing an inverted ms-timestamp into score low bits within the IEEE-754 53-bit integer budget; validates range headroom and returns an error if the score is too large to encode safely — never silently loses precision).

### 4.4 Rank tiers
- **Exact tier (all boards):** a board lives in one sorted set on one shard. `ZRANK`/`ZREVRANK` give exact rank in O(log N) regardless of size; top-N via `ZRANGE`/`ZREVRANGE`; `me ± k` via `ZRANK` then range. This fully covers the common case (many boards, each fits a node — incl. 100M on a large node) and scales horizontally **across** boards via Redis Cluster slot distribution.
- **Approx tier (seam, for a single board exceeding one node):** documented + interface-ready in SP1, implemented/benchmarked in a follow-on. Design: partition one board across shards by member-id hash; maintain a per-board score-distribution **bucket histogram** (`HINCRBY`) for O(1) approximate global rank; exact global **top-N** via k-way merge of per-shard top-N; `me ± neighbors` via score-window scatter-gather (exact local order, approximate absolute rank). Rank reads return `{Rank, Exact bool}` so callers see which tier answered. **We benchmark before building sharding** (research could not confirm the single-set breakpoint).

### 4.5 `RankingEngine` interface (Go)
```go
type RankingEngine interface {
    Submit(ctx, board BoardKey, member string, score float64) (SubmitResult, error)
    SubmitBatch(ctx, ops []SubmitOp) ([]SubmitResult, error)        // pipelined fan-out for SP2
    GetRank(ctx, board BoardKey, member string) (RankEntry, error)  // {Rank, Exact, Score}
    TopN(ctx, board BoardKey, n int) ([]RankEntry, error)
    Page(ctx, board BoardKey, offset, limit int) ([]RankEntry, error)
    Neighbors(ctx, board BoardKey, member string, k int) ([]RankEntry, error) // me ± k
    FriendRank(ctx, board BoardKey, members []string) ([]RankEntry, error)
    Count(ctx, board BoardKey) (int64, error)
    Remove(ctx, board BoardKey, member string) error
    Reset(ctx, board BoardKey) error                                // for window rollover
}
```

### 4.6 Error handling & edge cases
- Unknown member → `GetRank` returns `ErrMemberNotFound` (not rank 0).
- Empty board / out-of-range page → empty slice, no error.
- `firstToReach` overflow → `ErrScoreNotEncodable` at submit time.
- All write ops idempotent under `best`/`last` for the same (member, score); `increment` is **not** idempotent → SP2 layers a dedup key.
- Context cancellation/timeouts propagate from `go-redis`.

### 4.7 Testing strategy
- **TDD** against a **real Redis** in Docker (sorted-set semantics incl. `GT`/`LT` must be faithful — no fakes).
- Unit tests per behavior: each policy/sortOrder/tieBreak combo; rank exactness; neighbors window edges; friend rank ordering; reset; not-found; concurrent best-wins races.
- Property test: after N random submits, `GetRank` agrees with a brute-force sorted reference.
- Integration test: `DerivePhysicalBoards` fan-out + `SubmitBatch` pipeline correctness.

## 5. Non-goals (v1)
Billing/monetization; multi-region active-active; the custom Rust engine; UI dashboard beyond minimal admin. All are deliberate future seams, not omissions.
