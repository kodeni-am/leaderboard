# Running OpenLeaderboard on Redis Cluster

A single Redis node takes OpenLeaderboard a long way: reads are `O(log N)` on
sorted sets and a modest box sustains ~90k writes/s in the included load test.
Reach for Redis Cluster when one node can no longer hold all your boards in
memory, or when aggregate throughput across many boards exceeds what one node's
single thread can push. Cluster scales **across boards** — it spreads different
boards over different nodes. It does **not** by itself split one enormous board
across nodes; that is intra-board sharding (a separate feature).

## How the data lands on slots

Redis Cluster hashes every key to one of 16384 slots and assigns slots to
nodes. Keys only share a slot if they share a `{...}` hash tag.

- **Boards.** Each board's keys are built with a hash tag covering the whole
  board: `lb:{app:board:seg:win}:z`, `:h`, `:meta`. So all of a board's keys
  live on one node, and the multi-key operations the engine does for a board
  (the sorted set plus its metadata) never cross slots. Different boards hash to
  different tags and spread across the cluster.
- **Ingest streams.** The partitioned log writes to `lb:ingest:<p>` — one stream
  per partition. These deliberately have **no** shared hash tag, so the
  partitions spread across nodes and parallelize. The consumer therefore reads
  **one stream per `XREADGROUP`** (see `pkg/ingest/group.go`): a single command
  over multiple partition streams would span slots and Redis would reject it
  with `CROSSSLOT`. `Reclaim` (`XAUTOCLAIM`) is per-stream for the same reason.

The upshot: no application code changes between single-node and cluster. The
same binary works on both.

## Configuration

`REDIS_ADDR` accepts either a single `host:port` or a comma-separated list of
seed nodes. With more than one address the server uses go-redis's cluster
client; with one, the plain client.

```sh
# Single node (default)
REDIS_ADDR=redis:6379

# Cluster — list a few seed nodes (the client discovers the rest)
REDIS_ADDR=node-a:6379,node-b:6379,node-c:6379
```

Nothing else changes: `INGEST_PARTITIONS`, `WORKER_INDEX`/`WORKER_COUNT`, and
the rest of the env behave identically. More partitions give the cluster more
streams to spread, so set `INGEST_PARTITIONS` to at least the number of nodes
(a small multiple is fine).

## Caveats

- **One board never outgrows one node.** A board's entire sorted set lives on a
  single node because of its hash tag. If a *single* board's working set is too
  big for one node, cluster won't help — that needs intra-board sharding.
- **No cross-board atomicity.** There are no transactions spanning two boards,
  and there never were; each board is independent.
- **Client redirects.** Cluster clients follow `MOVED`/`ASK` redirects, so all
  seed nodes (and the nodes they point to) must be reachable from the server.
  On managed offerings (ElastiCache cluster mode, etc.) use the configuration
  endpoint.

## Testing against a real cluster

A skippable smoke test, `TestClusterIngestNoCrossSlot` in `pkg/ingest`, exercises
the cluster path. It is skipped unless `REDIS_CLUSTER_ADDRS` points at a cluster,
so the normal single-node suite stays green without one.

`deploy/docker-compose.cluster.yml` stands up a throwaway 6-node cluster
(3 masters + 3 replicas), forms it, and runs that test against it:

```sh
# from the repo root
docker compose -f deploy/docker-compose.cluster.yml up -d
docker compose -f deploy/docker-compose.cluster.yml logs -f clustertest   # watch for PASS
docker compose -f deploy/docker-compose.cluster.yml down -v
```

To point the test at your own cluster instead:

```sh
REDIS_CLUSTER_ADDRS=node-a:6379,node-b:6379,node-c:6379 \
  go test ./pkg/ingest/ -run TestClusterIngestNoCrossSlot -v
```
