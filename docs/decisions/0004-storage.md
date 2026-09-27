# ADR-0004: Storage for the event log and snapshots

- **Status:** Accepted
- **Date:** 2026-09-27

## Context
Every edit is stored as an event. Storage has to support:
- full history, with replay and point-in-time restore, at low cost,
- no lost acknowledged edits, including across server crashes,
- running on a free-tier VM and self-hosting with one command.

## Options
| Option | Pros | Cons |
|---|---|---|
| **Postgres for everything** | Boring and well understood. One dependency. Transactions. Easy backups with `pg_dump`. | Row overhead per op is high until ops are compacted. |
| **Postgres + S3-compatible store for segments and snapshots** | Cheapest at large scale. | Adds MinIO for self-hosting. Two systems to back up. |
| **Kafka / Redpanda / NATS JetStream as the log** | Built for append-only logs. | Overlaps with MilkRun. Heavy on a free VM. Still needs a snapshot store. |
| **Embedded per node (SQLite / Pebble)** | Fastest writes, nothing to operate. | Another node can't read the data, which makes failover and board migration hard. |

## Decision
**Postgres only for v1.** Large blobs go through a `BlobStore` interface, with Postgres `bytea` as the v1 implementation; an S3 implementation is later scope. Five mechanisms:

1. **Hot log**
   - Table: `ops(board_id, seq, client_id, client_seq, batch)`. `batch` is a protobuf `StoredBatch` holding the stamp and ops.
   - Primary key `(board_id, seq)`. It is also the fence against two writers for one board (ADR-0005).
2. **Group commit and acks**
   - Each tick (~20 ms), the board actor writes every batch it has accepted in one `COPY`, which is a single atomic statement.
   - Only after that commit does it acknowledge the batches to their senders and broadcast them to other clients.
   - So no client ever sees an edit that could be lost.
   - If the commit fails, the board drops all clients and unloads (crash-only). Clients still hold their unacked edits and resend them to the reloaded board.
3. **Idempotent resend**
   - A client resends unacknowledged batches after reconnecting.
   - The board tracks the highest `client_seq` applied per client. It rebuilds that map on load from the snapshot's `clients` list plus the log tail.
   - It acks resends again without reapplying them, and `Welcome.last_client_seq` tells the client which batches to drop.
   - *Revised in W2:* the plan was a unique index on `(board_id, client_id, client_seq)`. We dropped it, because the in-memory map already does the job and the index would roughly double index storage per edit.
4. **Compaction**
   - Once a board's ops are older than the latest snapshot, they're packed into `op_segments(board_id, from_seq, to_seq, blob)`, about 10k ops per segment, as protobuf compressed with zstd.
   - The individual op rows are then deleted.
5. **Snapshots and restore**
   - A full board snapshot (protobuf plus zstd) is taken every 5,000 ops or 10 minutes of activity, whichever comes first.
   - Snapshots are the keyframes for history replay.
   - A point-in-time restore appends ops that recreate the old state. History stays append-only, and a restore can itself be undone.

## Consequences
- **Commit is on the latency path:** sync latency includes the group-commit time, so it's measured and reported.
- **Storage cost is measurable:** raw op rows, compacted segments, and snapshots are separate tables, so storage per edit can be broken down (see BENCHMARKS.md).
- **One backup:** backing up means backing up Postgres only.
- **Snapshot retention:** keep every snapshot for the most recent 7 days, then thin to one per day. This is tunable. Segments are always kept, so full history survives.
