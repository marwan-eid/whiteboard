# A whiteboard that only loads what you look at

This is a write-up of how this collaborative whiteboard works, what was measured, and what broke along the way. The code is in this repository, the live demo is linked from the [README](../README.md), and the design decisions are in [docs/decisions](decisions/).

## The goal

The board had to meet four requirements:
- Many people edit one board in real time, with an edit appearing for everyone else in well under 100 ms.
- A board can hold 100,000 shapes, and a browser only downloads the part it is looking at.
- Every version is kept, can be replayed, and can be restored.
- It runs for $0 and self-hosts with `docker compose up`.

The last requirement ruled out most managed services. The second ruled out the usual collaborative-editing libraries.

## Why not Yjs or Automerge

CRDT libraries like Yjs and Automerge are the default answer for collaborative editing, and they are excellent. They have two properties that did not fit here:
- **Every client holds the whole document.** A 100,000-object board would be downloaded and kept in memory by every browser, even one looking at a single sticky note.
- **The server sees opaque bytes.** It cannot filter by viewport, validate edits, enforce permissions, or rate-limit by what an edit does.

So the sync model is custom ([ADR-0001](decisions/0001-sync-model.md)). Each object is a map of properties. Each property is a **last-writer-wins register** ordered by a **hybrid logical clock** stamp `(wall ms, counter, client id)`. Merging is commutative and idempotent, so replicas converge whatever order edits arrive in, including edits made offline. On top of that, the server gives every accepted batch a sequence number per board. That gives one total order for the log, for history and for replay.

There is exactly one kind of operation: "set these properties". Creating an object sets its type, and deleting one sets `deleted = true`. Stacking order uses fractional indexes, so moving one object to the front never rewrites the others.

The trade-off is explicit: two people typing in the same sticky note at the same moment end up with one person's text, not a merge. That is documented as a known limit.

## One goroutine per board

Each live board is an actor: a single goroutine that owns the board's state. Every 20 ms it ticks:
1. It validates the batches that arrived since the last tick.
2. It assigns them sequence numbers and applies them.
3. It writes them to Postgres in one `COPY` (group commit).
4. **Only then** does it acknowledge them to their senders and send them to everyone else.

No one ever sees an edit that could still be lost. If a commit fails, the board does not try to patch itself up. It drops its clients and reloads from the database, and clients resend whatever was never acknowledged (crash-only design). A unique key on `(board, client, client seq)` makes those resends idempotent.

Fan-out is "encode once". Each operation is serialized once per tick, and each client's frame is assembled by copying the pieces it needs. Frame building runs in parallel across workers, since it only reads board state.

## The server decides what each client holds

Viewport interest ([ARCHITECTURE.md](ARCHITECTURE.md), "Viewport interest") was the hardest part to get right. Each client subscribes to a rectangle: its visible area plus a margin. For every edit, the server knows whether the object was in a client's view before the edit and after it:
- **In view before:** the client holds the object, so it gets the edit as a delta.
- **Only in view after:** the client lacks the object, so it gets the whole object.
- **In view before but not after:** the client is told to drop it.

Early versions let clients evict objects on their own when they scrolled away. Randomized tests, with random edits, random viewport changes and random message delays, found races where a client and the server disagreed about what the client held. The client would then apply a delta to an object it no longer had, or never receive an object it needed. The fix was to make the server the only authority:
- Clients only drop what the server tells them to.
- Each edit is judged against the viewport that was in force when it happened, not the latest one.

One more rule closes a gap: a client keeps an object it was told to drop if it still has unacknowledged edits to it. The server never echoes a client's own edits, so dropping the object would lose them.

Zoomed far out, clients switch to a level-of-detail mode. They receive only boxes and colors, and the browser draws them as GPU particles. On a laptop with integrated graphics, panning a 100,000-object board took 5.9–6.8 ms per frame at p95 (the bar for 60 fps is 16.7 ms). The browser held 19–242 objects at normal zoom ([results](../benchmarks/results/2026-10-02-browser-100k.md)).

## History that costs 31 bytes per edit

The operation log is the history:
- **Compaction:** old entries are packed into zstd-compressed segments.
- **Snapshots:** the server snapshots the board every 5,000 batches, and at most once a minute on busy boards.
- **Scrubbing:** "the board at version N" is rebuilt from the nearest snapshot or cached keyframe, plus replay. The history slider is debounced and does not block live editing.
- **Restore:** a restore appends the difference as a new batch, so history stays append-only and a restore can itself be undone.

Measured with a million edits of the benchmark mix, full history costs **31.3 bytes per edit**, against 172 for the raw log ([results](../benchmarks/results/2026-09-28-storage.md)).

## Access, and edits that survive a reload

Guests get an identity with no signup: `<id>.<HMAC of id>`, kept in local storage. A board made with "New private board" can only be opened by its owner and by holders of a share link, either view or edit.
- **Link storage:** links are stored only as SHA-256 hashes.
- **Revocation:** revoking a link disconnects everyone using it immediately. The node that handled the revoke request announces it to every node through Postgres `NOTIFY`, because the board may live on a different node.
- **Viewers:** the server enforces view-only access, not just the UI.

Unsynced edits are written to IndexedDB, so they survive a reload or a closed tab while offline. Each browser tab works under its own client id per board, held with a Web Lock, so two tabs never collide. A tab that opens later takes over an unheld id together with its queued edits. One subtle bug was ruled out by test: a reused id must never repeat a client sequence number the server has already seen. If it did, the server would acknowledge the new batch as a duplicate and silently drop it.

## A bug from week one, found in week seven

A randomized convergence test failed for one seed:
1. Two clients ended up with different values for the same property under the **same** timestamp.
2. The cause: a batch can contain two operations on the same object (say, "not deleted" then "deleted"), and every operation in a batch carries the batch's timestamp. Replicas apply operations in order, and an equal timestamp never overwrites.
3. A client that received the batch's second operation as a delta, and the object in full, kept the other value.

The bug had been there since the first sync milestone. The fix is to merge each batch to one operation per object, with later properties winning, in both the Go and TypeScript code. The server logs the merged batch. A shared test vector, run by both languages, now pins the rule.

## Several nodes, one owner per board

A board must have exactly one writer, because it assigns the sequence numbers. With several nodes ([ADR-0005](decisions/0005-board-placement-routing.md)):
- **Membership:** nodes heartbeat into Postgres.
- **Placement:** rendezvous hashing over the live nodes picks each board's preferred node.
- **Ownership:** a node serves a board only while it holds the board's **lease**, which has an epoch.
- **Routing:** clients ask which node serves a board, connect to it through Caddy at `/n/<node>/ws`, and follow a `Moved` reply if the answer was stale.

The plan relied on fencing: if a stale owner tries to commit, its insert hits the `(board, seq)` primary key and fails. A test where the owner stopped renewing its lease, but kept its sockets, showed that fencing alone is not enough. The stale owner kept committing first, so the *new* owner's commits were the ones that failed, again and again: safe, but no progress.

The fix: each board tracks when its lease runs out, as of the start of the last successful renewal, and stops serving once it might have expired. Fencing still covers the rare case of a process paused between that check and a commit. Only nodes that are themselves heartbeating may claim leases.

The Compose stack runs two nodes. An end-to-end test kills the node serving a board with SIGKILL, mid-edit, and leaves it dead. In a local run, every client was back on the other node 6.7 s later (with 5 s leases), and all 2,739 acknowledged batches were in Postgres.

## Load testing on free runners

The load generator runs simulated editors on a fixed scenario:
- One op per second each: drag bursts at 10 Hz, creates, text edits and deletes.
- Cursors at 15 Hz.
- **Half of all editors in one shared viewport.**

Latency is measured inside one process, from the sender writing an edit until another simulated editor receives it, so no clock sync is needed. The rig is free GitHub Actions runners: the server (one node and Postgres) gets 2 cores and the load generator the other 2. Three runs per editor count ([results](../benchmarks/results/2026-10-02-sync.md)):

| Editors | Median p99 |
|---|---|
| 100 | 21.8 ms |
| 250 | 24.6 ms |
| 500 | 58.4 ms |
| 1,000 | 231 ms |

**The 1,000-editor target (p99 under 100 ms) was not reached.** With half of 1,000 people in one view, every edit goes to about 500 receivers, and every client gets a frame each tick. On two cores, the per-frame socket writes and the scheduling of 1,000 writer goroutines produce a tail. A 50 ms tick brought p99 to 123 ms, at the cost of a slower median.

**What profiling fixed along the way:**
- **Nearest cursors** are now computed once per view and shared, instead of once per client. They had been the largest part of frame building.
- **Write deadline contexts** are reused across writes. Creating one per message cost 7% of CPU.
- **Snapshots on busy boards** are limited to once a minute. Before, each one stalled the board for a moment every few seconds.
- **The load generator** got lighter. Its first version spent its CPU keeping replicas it never read, so its 1,000-editor runs were invalid by the method's own rule (load generator CPU under 70%).

Over the internet, from a laptop to the live demo (on the small fallback VM, about 62 ms away), 50 editors saw p50 99.6 ms and p99 230 ms ([results](../benchmarks/results/2026-10-02-wan.md)).

## Running it for $0

The demo runs on Oracle Cloud's Always Free tier, built with Terraform in its own compartment ([docs/DEPLOY.md](DEPLOY.md)):
- Caddy for TLS, Postgres, Prometheus and Grafana.
- Images published to GHCR by CI.
- Nightly `pg_dump`s uploaded through a write-only link, so the server holds no cloud credentials.

Two things did not go to plan:
- The account's free ARM allowance turned out to be half the expected size.
- When provisioning, Oracle had no ARM capacity in the region.

Following the hosting ADR's fallback, the demo runs on the free 1 GB x86 VM while the ARM VM is retried. A restore drill on the live server wiped every volume and restored from Object Storage; the boards' fingerprints (a SHA-256 over every object's state) were identical afterwards.

## What I would do next

- **Make the 1,000-editor tail shorter:** batch several frames into one socket write, use a tick that lengthens only under load, or fan out through edge nodes so one process does not write to every socket.
- **Merge concurrent text edits:** a CRDT per text object instead of last-writer-wins.
- **Retention:** thin out old snapshots, and stop anyone from creating unlimited public boards just by visiting new URLs.
