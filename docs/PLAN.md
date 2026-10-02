# Plan

Pace: about 20 hours a week, solo. Each week ends with something demoable locally. Every milestone ships with its tests: unit tests, integration tests for sync and persistence, and concurrency and failure tests.

## Progress
| Milestone | Status | Notes |
|---|---|---|
| W0 Scaffolding | Done 2026-09-27 | |
| W1 Sync core | Done 2026-09-27 | Go and TS merges agree on shared vectors. Randomized convergence passes over real sockets with drops and offline periods. A Playwright test covers two browsers syncing. |
| W2 Durability | Done 2026-09-27 | Group commit before ack; crash-only board failure; snapshots plus log-tail load. Randomized crash tests (in-memory store and Postgres), a real SIGKILL test (Go), and a browser restart test are in CI. |
| W3 Usable editor | Done 2026-09-27 | Six shape types, arrows attached to shapes, select, marquee, move, resize, pan and zoom, in-place text, undo and redo, live cursors. The client now renders on demand. Browser tests cover every tool. |
| W4 Scale on one board | Done 2026-09-27 | Viewport interest decided by the server (enter and leave), LOD particles, nearest-30 cursors, encode-once and parallel fan-out, seed tool, load generator v1. Local pre-check (not a benchmark): [benchmarks/results/2026-09-27-local-w4.md](../benchmarks/results/2026-09-27-local-w4.md). |
| W5 History | Done 2026-09-28 | Compaction into zstd segments, history at any version (snapshot or keyframe plus replay), slider UI, point-in-time restore (undoable). Storage measured: 31.3 B/edit with full history at 1M edits, against 172 B/edit for the raw log ([results](../benchmarks/results/2026-09-28-storage.md)). Snapshot retention thinning is not done yet. |
| W6 Access and offline | Done 2026-09-28 | Signed guest identity, private boards, share links (hashed, revocable, and revoking disconnects at once), roles enforced by the server, "my boards". Limits: per-connection throttling, per-IP sockets and board creation, 200k objects per board. Unsynced edits persist in IndexedDB under a per-tab client id (Web Locks). Browser tests cover links, revocation, and a reload while the server is down. Not limited yet: public boards created by visiting new ids. |
| W7 Timers, voting, live stats | Done 2026-10-02 | Board-wide objects (reserved ids, delivered to every viewer, validated by type) carry a shared countdown timer and dot voting. The timer counts down on the estimated server clock; a browser test runs one browser with its clock an hour fast and both show the same time to within a second. Live stats panel over server-sent events. Also fixed a convergence bug from W1, found by the randomized test: a batch writing one property twice could leave replicas with different values (batches are now coalesced). Timer skew under load is not measured yet. |
| W8 Scale-out | Done 2026-10-02 | Node membership by heartbeat, rendezvous placement, board leases with epochs, renewed in one batch; a board stops by itself once its lease may have expired, and the (board_id, seq) fence covers the rest. Route API, `/n/{node}/ws` and `Moved`; Caddy spreads `/ws` and `/api` over two nodes. Revocations cross nodes through Postgres NOTIFY. Tests: killed owner (no acknowledged edit lost), paused owner (steps down), two writers (fenced), and a Compose e2e that SIGKILLs the owning node. Local, not a benchmark: clients back on the other node 6.7 s after the kill with 5 s leases. The chaos button in the UI was cut. |
| W9 Load test (Phase 4) | Done 2026-10-02 | Bench rig on free GitHub Actions runners (`.github/workflows/bench.yml`), 18-run sweep. **The 1,000-editor target was not reached:** median p99 21.8 ms at 100 editors, 58.4 ms at 500, 231 ms at 1,000 (123 ms with a 50 ms tick). Profiling led to five fixes (shared cursor fan-out, write deadlines, snapshot rate, a lighter loadgen); the remaining bottleneck is per-frame socket writes on 2 cores. Browser: pan p95 under 7 ms with 100k objects on a laptop GPU. WAN run and GIF after deployment. Results: `benchmarks/results/2026-10-02-*.md`. |

## v1 scope
1. Unlimited persistent boards. Shapes: rect, ellipse, sticky, text, arrow, freehand.
2. Full version history: a replay slider and point-in-time restore.
3. Private boards with share links (view or edit). Guests can edit without signing up.
4. Synchronized timers and voting.
5. Self-hosting with one command (`docker compose up`).
6. A public demo board with a live metrics panel and a chaos control (kill a node).

## Weeks

| Week | Milestone | Key work | Demo at end of week |
|---|---|---|---|
| **W0** | Scaffolding (Phase 2) | Monorepo (Go in `cmd/` + `internal/`, `web/`, `proto/`, `deploy/`, `docs/`); Protobuf codegen with `buf`; Compose stack (Caddy, one node, Postgres); GitHub Actions for lint and tests (Go and TypeScript); A1 allowance confirmed free: MilkRun runs on the AMD Micro shape (ADR-0006). | `docker compose up` serves an empty canvas that connects over WebSocket. CI is green. |
| **W1** | Sync core | Object model; ops; HLC; LWW merge (Go and TS, sharing test vectors); board actor with tick loop; optimistic client store; property-based convergence tests covering random order, duplicates, and partitions. | Two browser tabs create and drag shapes concurrently and converge. |
| **W2** | Durability | Group commit; ack after commit; pending-op queue with resend; idempotent dedupe; snapshots; board load from snapshot plus log tail; integration tests on a real Postgres (testcontainers). | Kill the server mid-drag and restart it. Nothing acked is lost, and unacked ops are resent. |
| **W3** | Usable editor | All v1 shapes; select and multi-select; move and resize; pan and zoom; arrow bindings; text editing; local undo and redo; live cursors. | A real whiteboard session between two machines. |
| **W4** | Scale on one board | Spatial grid; viewport subscriptions and diffs; LOD mode; interest-filtered fan-out with pre-encoded frames; presence filtering (K nearest); bounded send queues and `Resync`; seed tool for 100k objects; loadgen v1; Prometheus metrics. | Pan smoothly around a 100k-object board. A local bot run with the first recorded numbers, labeled as a local run. |
| **W5** | History | Compaction into segments; snapshot scheduling and retention; history worker with keyframe LRU; replay slider with play; point-in-time restore (appended as ops); script that measures storage per edit. | Scrub the replay slider through a busy board, then restore to 10 minutes ago and undo the restore. |
| **W6** | Access and offline | Guest tokens; private boards; share links (hashed, revocable); "my boards" list; rate limits (connection, IP, board); IndexedDB offline queue with bounds. | A guest joins through a view link and then an edit link; a revoked link stops working. An offline edit merges on reconnect. |
| **W7** | Timers, voting, metrics panel | Clock-offset estimation; timer ops; voting sessions; public stats stream; metrics panel UI. | A timer stays in sync across a phone and a laptop. A dot-voting round. The live panel. |
| **W8** | Scale-out | Node membership; rendezvous placement; leases with fencing; `/route` and `MOVED`; Caddy routing by path; failover; chaos button; failover tests (paused owner, killed owner, network split between node and Postgres). | Kill the owner node during editing. Clients move to another node with zero lost acked edits. |
| **W9** | Load test (Phase 4) | Run [BENCHMARKS.md](BENCHMARKS.md) on the GitHub Actions rig, plus a WAN run against the demo; profile with pprof; fix bottlenecks; rerun; record results with the exact setup. | A results table plus the load-test GIF. |
| **W10** | Deploy (Phase 5) | Terraform (OCI); cloud-init; production Compose; Grafana dashboards; nightly backups plus a restore drill; self-hosting guide. | The public demo is live over HTTPS, and a restore from backup has been verified. |
| **W11** | Launch (Phase 6) | README with the demo link and GIF; architecture write-up (blog-length); list of places to share it; daily-active-users counter. | Launched. |

**Buffer:** expect 20–30% slippage. W4 and W8 are the heaviest weeks. If time is short, cut in this order:
1. Voting
2. LOD polish
3. The chaos button in the UI (keep the scripted chaos test)

**Early risk work:** try to create the A1 VM early, since A1 capacity can take retries. W4's first bot tests run locally or on GitHub Actions, so they don't depend on Oracle.

## Later scope (not in v1)
- **Collaboration features:** presentation mode (others follow the presenter's view); WebRTC voice (needs a TURN server, which costs bandwidth, so it will likely be limited); AI features (bring-your-own key, since there's no budget for inference).
- **Editor:** per-object text CRDT for concurrent typing in one sticky; groups, frames, rotation; image uploads (needs object storage); export to PNG and SVG.
- **Scaling:** two-tier fan-out through edge nodes for boards beyond one node's fan-out capacity.
- **Storage and ops:** S3 `BlobStore` for segments and snapshots; Postgres replica for high availability.
- **Accounts:** GitHub sign-in to claim guest boards across devices.
