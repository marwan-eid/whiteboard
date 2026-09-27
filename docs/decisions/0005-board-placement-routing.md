# ADR-0005: Board placement and client routing

- **Status:** Accepted
- **Date:** 2026-09-27

## Context
The ADR-0001 model needs exactly one board actor per board, the single writer that assigns `seq`. With several nodes, three things must hold:
- each board lands on one node,
- clients find that node,
- if a node dies, another takes over without split-brain (two nodes writing the same board) and without losing acknowledged edits.

## Options
| Option | Pros | Cons |
|---|---|---|
| **Single node** | Trivial. | No scale-out or failover story. |
| **External coordinator (etcd / Consul) with leases** | Purpose-built consensus. | One more stateful system to run and self-host. |
| **Postgres-backed membership and board leases, with fencing** | No new dependency, and it uses Postgres transactions. | Lease timing is bounded by Postgres availability. Postgres is already a single point of failure in v1. |

## Decision
**Use Postgres-backed membership and leases.**

1. **Membership**
   - Each node upserts `nodes(id, addr, heartbeat_at)` every 1 s.
   - A node is live if its heartbeat is less than 3 s old.
2. **Placement**
   - Rendezvous (highest-random-weight) hashing of `board_id` over the live nodes picks the preferred owner.
   - When nodes join or leave, the preference changes for only about 1/N of boards.
3. **Ownership**
   - The owner holds a row in `board_leases(board_id, node_id, epoch, expires_at)` and renews it every 2 s, with a 5 s TTL.
   - Acquiring the lease is a conditional UPDATE or INSERT that increments `epoch`.
4. **Fencing**
   - Every commit writes `seq` values that follow the last `seq` the owner knows.
   - A stale owner's INSERT fails on the `(board_id, seq)` primary key. That owner then drops the board and closes its sockets.
5. **Routing**
   - The client calls `GET /api/boards/{id}/route` on any node and gets back `{nodeId}`.
   - It then connects to `wss://host/n/{nodeId}/ws`, and Caddy proxies that path to the node.
   - A node that receives a connection for a board it doesn't own replies `MOVED`, and the client re-routes.
6. **Failover**
   - The owner dies, and clients' sockets close.
   - Clients re-route after jittered exponential backoff.
   - The new preferred owner waits for the lease to expire and acquires it.
   - It loads the latest snapshot and the log tail after it.
   - Clients resend unacknowledged ops, which the unique key deduplicates.

## Consequences
- **One board on one node:** 1,000 sockets is well within one Go process. If benchmarks show that one node's fan-out is the bottleneck, the next step is two-tier fan-out through edge nodes (later scope).
- **Failover delay:** worst case, the board is unavailable for about the lease TTL plus reconnect backoff. This is measured in BENCHMARKS.md.
- **Postgres is a single point of failure.** Accepted for v1 and documented.
