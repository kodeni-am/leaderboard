# Horizontal scale: Redis Cluster, approximate rank, intra-board sharding

Date: 2026-06-18
Status: implemented

This design covers three scaling steps, in the order they were built. Each is
independent and opt-in; the default single-node behavior is unchanged.

## 1. Redis Cluster (scale across boards)

**Goal.** Spread *different* boards across cluster nodes so total capacity and
throughput exceed one node.

**Design.**
- Board keys already carry a hash tag covering the whole board
  (`lb:{app:board:seg:win}:z|h|meta`), so each board's keys co-locate on one
  slot and the engine's per-board multi-key ops never cross slots.
- The ingest streams `lb:ingest:<p>` deliberately have **no** shared tag, so
  partitions spread across nodes. The consumer therefore reads **one stream per
  `XREADGROUP`** (a single multi-stream read would span slots → `CROSSSLOT`).
  `Reclaim`/`XAUTOCLAIM` is likewise per-stream. `Run` polls when idle because
  the per-stream reads are non-blocking.
- `REDIS_ADDR` accepts a comma-separated seed list; >1 address makes go-redis
  select its cluster client. No other code changes between single-node and
  cluster.

**Caveat.** A single board's sorted set still lives on one node (that's what
step 3 addresses).

**Tested.** `TestClusterIngestNoCrossSlot` (skipped unless
`REDIS_CLUSTER_ADDRS` is set) plus `deploy/docker-compose.cluster.yml`, which
forms a throwaway 6-node cluster and runs the test against it. Verified green on
a real 6-node cluster.

## 2. Approximate-rank read tier

**Goal.** Estimate a member's global rank without a rank scan, as the building
block sharding needs.

**Design.**
- A per-board fixed-bucket score histogram in a Redis hash (`:h`), maintained on
  writes via `HINCRBY`. `BoardConfig` gains `ApproxRank`, `ApproxMin`,
  `ApproxMax`, `ApproxBuckets` (default 1024).
- On write, the engine reads the pre-write stored score in the same pipeline,
  then in a follow-up pipeline moves the member between buckets (add for new,
  remove-old + add-new on change, no-op when a `best` write doesn't improve).
  `Remove` decrements the member's bucket. Accuracy assumes per-member writes
  are serialized — which the ingest log guarantees, since it partitions by
  member.
- `GetApproxRank` sums "ahead" buckets in `O(buckets)` and returns
  `{rank, exact:false}`. Exposed over HTTP as `?approx=true` and in both SDKs as
  `getApproxRank`.

**Resolution.** Rank is accurate to one bucket width: `0 <= exact - approx <
(member's bucket population)`.

**Tested.** Unit tests for desc/asc/increment/best-no-op/remove/disabled, plus a
property test asserting the bucket-resolution bound on a dense random
distribution. API-level test for `?approx=true`.

## 3. Intra-board sharding (`ShardedEngine`)

**Goal.** Split one board across N sorted sets so a single board can exceed one
node.

**Design.**
- `BOARD_SHARDS=N` selects `ShardedEngine`, which implements the same
  `RankingEngine` interface. Members map to a shard by FNV-1a hash; each shard
  is the board with its name suffixed (`board#sN`), giving it a distinct hash
  tag/slot. Config (including the histogram) is propagated, so each shard keeps
  its own histogram.
- **Exact** ops: submit/remove (routed), count (sum), top-N and page (k-way
  merge of each shard's top range — the global top-K is a subset of the union of
  per-shard top-Ks), friends (scores gathered, ranked within the set).
- **Approximate** rank: sum the per-shard histograms (read in one pipeline) and
  compute "ahead". Sharded boards must enable `ApproxRank`.
- **Neighbors**: score-window scatter-gather — pull k+1 entries on each side of
  the member's score from every shard, merge, locate the member, slice ±k.
  Membership/order is exact; absolute rank numbers are anchored on the
  approximate rank (`exact:false`).

**Empirical findings** (single Docker Redis, laptop — not production numbers):
- Writes stay at ~83k/s regardless of shard count (sharding scales writes and
  memory across nodes).
- Top-N/page/neighbors match the single-set engine exactly (verified by tests
  that diff sharded vs single-set results over identical data).
- Approximate rank reads cost ~`O(shards × buckets)` per call (one pipelined
  round trip), independent of member count once buckets saturate. Fewer buckets
  → faster but coarser. For rank-read-heavy workloads, cache the merged
  histogram (future work).

**Tested.** `sharded_engine_test.go` diffs sharded results against a single-set
`RedisEngine` ground truth for top-N/page/count/friends/neighbors (exact), checks
the approx-rank bound, firstToReach tie order across shards, stable routing, and
best-policy updates. `cmd/loadtest -shards N` benchmarks the sharded path.

## What is intentionally not built

- Exact global rank across shards (a board big enough to shard is past the point
  where an exact global scan is cheap; the histogram estimate is the trade).
- A cached/merged histogram for faster sharded rank reads.
- Resharding/rebalancing when N changes (changing `BOARD_SHARDS` rehashes
  member→shard routing; treat it as a one-time capacity decision, like
  `INGEST_PARTITIONS`).
