# Project Brief: Real-Time Collaborative Whiteboard

You're helping me plan, then build, this project. **Start with planning only. Don't write application code until I approve the plan.**

---

## About me

- Backend software engineer, about 4 years of experience.
- This is a solo, part-time side project with zero hosting budget.

## Why this project

It's a resume project for international backend roles. It must involve real system design problems, produce measurable results, have a live public demo, and ideally attract real users. It should not overlap with my other project, MilkRun (Kafka-based real-time fleet tracking).

## The product

An open-source, self-hostable, real-time collaborative whiteboard (Miro/Excalidraw-style). It offers free the features the major tools paywall, and it's engineered to handle far more than typical boards, when possible.

**v1 features:**
1. Unlimited persistent boards.
2. Full version history with a replay slider and point-in-time restore.
3. Private boards with share links; guest editing with no signup.
4. Synchronized timers and voting.
5. Self-hostable with one command.

**Later candidates:** presentation mode (others follow the presenter's view), voice via WebRTC, AI features.

## Engineering targets (targets, not claims)

- 1,000 concurrent editors on one board, sync under 100 ms p99.
- Smooth with 100,000 objects on one board, loading only the visible region.
- Offline edits merge cleanly on reconnect.
- Every edit stored as an event; periodic snapshots plus compacted change logs keep full history cheap.
- Reconnection without losing edits; durable persistence.

## Problems the design must address

- Conflict resolution for concurrent edits on a structured canvas.
- Fan-out to 1,000 clients; presence (live cursors) at that scale, sending each user only what's near their view.
- Persistence: event log, snapshots, compaction, and the storage cost of full history.
- Spatial indexing so clients load only their viewport.
- Scaling out: placing boards on servers and routing clients to the right one.
- Auth and permissions: share links, anonymous guests, abuse and rate limits.
- Offline sync and merge.
- Timers synchronized across devices despite clock differences.

## Demo and measurement

- Public board that loads instantly, no signup.
- Live metrics panel: connected users, messages per second, sync latency.
- History replay slider as the showpiece.
- A recorded load test with thousands of simulated editors, as a short GIF for the README.
- Optional chaos control: kill a server and watch clients reconnect without losing edits.

**Numbers I'll need to report at the end** (all must be measured, never estimated):
- Concurrent editors on one board, and sync latency p99.
- Storage used per number of edits, with history enabled.
- Real users, once deployed.

## What I want from you in the planning phase

1. **Clarifying questions first.** Ask up to 5, and only if the answers would change the plan.
2. **Key decisions.** For each of these, give 2-3 options with trade-offs and a recommendation, then record the decision in `docs/decisions/` (one short ADR each):
   - sync model: a CRDT library such as Yjs or Automerge, or a custom design
   - backend language and framework
   - canvas approach: build on an open canvas SDK, or custom rendering
   - storage: where the event log and snapshots live
   - board placement and client routing
   - hosting on a small budget
3. **`docs/ARCHITECTURE.md`**: components, data model and data flow, with Mermaid diagrams.
4. **`docs/PLAN.md`**: v1 scope, and a week-by-week plan where each week ends in something demoable, plus a list of later scope.
5. **`docs/BENCHMARKS.md`**: how we'll measure each target, including how to run a 1,000-editor load test cheaply.
6. **Only after I approve all of the above:** move to phase 2 below.

## The full project cycle

We work in phases. **At the end of each phase, stop, summarize what was done and what's next, and wait for my approval before continuing.**

1. **Planning and architecture:** the deliverables listed above.
2. **Scaffolding:** repo layout, local dev setup (one command to run everything), CI running lint and tests on every push.
3. **Development, one milestone at a time** from `docs/PLAN.md`. For each milestone:
   - write the code together with its tests: unit tests, plus integration tests for sync and persistence
   - add tests for concurrency and failure cases (simultaneous edits, disconnects, reconnects), since those are the point of the project
   - end with something I can demo locally, and update the docs if the design changed
4. **Load testing and benchmarks:** run the plan in `docs/BENCHMARKS.md`, record the results with the exact setup used, and fix the bottlenecks they reveal.
5. **Deployment:**
   - infrastructure as code
   - a production deploy of the public demo
   - observability: metrics, logs, and the live metrics panel
   - backups for board data
   - a one-command self-hosting guide
6. **Launch:** README with a demo link and load-test GIF, architecture write-up, and a short list of where to share it to find real users.

## Working rules

- Never present a target as a result, or report a number that wasn't measured.
- Prefer boring, well-understood technology unless the unusual choice is the point of the project.
- Be concise and concrete.
- If something I ask for conflicts with these goals, tell me. I know some of the features I ask for might be hard with zero budget, and this fine; for those, we would have to cut short on some of the numbers/features then.