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

## Implementation notes (W8)
Built in `internal/cluster` and `internal/board` (`placement.go`). What changed from the plan, and why:
- **Leases are checked locally too.** Fencing on `(board_id, seq)` keeps the log correct, but on its own it does not stop a stale owner: in a test where the owner stopped renewing but kept its sockets, it kept committing first, and the new owner's appends were the ones that failed, again and again. Now each board knows when its lease runs out, as of the start of the last successful claim or renewal plus the TTL, and stops before committing after that. The lease in Postgres lasts at least as long, because it is set from the database clock after the request was sent. Fencing still covers a process paused between that check and a commit.
- **Only live nodes take or keep leases.** Claiming and renewing require the node's own heartbeat to be recent, checked in the same SQL statement, so a node the others consider dead cannot grab boards with a stale view of the cluster.
- **A board's lease belongs to that board object.** The registry claims before creating a board, and an unloading board releases its own epoch, so a release can never cancel a newer claim.
- **Routing falls back to any node.** Clients ask `GET /api/boards/{id}/route`, connect to `/n/{node}/ws`, and follow `Moved`. If the node they were sent to is unreachable, they start again from `/ws`, which Caddy sends to any live node. That node redirects, or claims the board once the old lease has expired.
- **Board events cross nodes through Postgres `NOTIFY`.** Revoking a share link is handled by whichever node got the HTTP request, but the board may live on another node. Every node listens on one channel and kicks its own connections. Delivery is best effort; a missed event only delays the kick until the user reconnects, since joins are authorized against the database.
- **Not built:** the chaos button in the UI (on the plan's cut list). Failover is covered by scripted tests instead.

Tested by `internal/cluster`: leases and redirects; a killed owner, with no acknowledged edit lost; and a paused owner that steps down. Also by a board-level test of the fence, and by `test/e2e/kill_test.go`, which SIGKILLs the owning node of the Compose stack and does not restart it. Local results, which are not benchmarks:
- With 5 s leases on Docker Desktop, clients were back on the other node 6.7 s after the kill. All 2,739 acknowledged batches were in Postgres.
- In process, with 600 ms leases, clients were back in 0.87 s.
