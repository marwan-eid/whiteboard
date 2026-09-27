# Benchmarks

This doc defines how each engineering target is measured. **Nothing here is a result yet.** Results go in `benchmarks/results/YYYY-MM-DD-<name>.md`, each with the full setup record described below. A number that wasn't measured is never reported.

## Setup record (required with every result)
- Commit hash, Go version, browser version where relevant
- Machines: shape, vCPU, RAM, region, and whether they share a network (VCN)
- Server config: node count, tick interval, `GOGC`, Postgres settings
- Scenario file (checked in under `loadgen/scenarios/`) and the exact command line
- Run duration and warm-up period excluded from results
- The raw histogram files (HDR `.hlog`), committed alongside the summary

## Targets and methods

### 1. 1,000 concurrent editors on one board, sync p99 < 100 ms
**Definition:** *sync latency* is the time from client A handing an op to its socket until client B has decoded it. It is measured for every (sender, receiver) pair where the receiver's viewport contains the object.

**Clock-free measurement:**
- Each loadgen process stamps its outgoing ops with its own monotonic clock.
- It records latency only when one of *its own* simulated clients receives the op.
- Sender and receiver therefore share a clock, and runs across several machines stay valid with no clock sync.

**Recording:** HDR histograms, reporting p50, p90, p99, p99.9, and max.

**Also recorded:**
- Server-side latency: from receive to the frame being written to the socket.
- Commit duration.
- Tick duration.
- Server CPU and memory.
- Frames dropped due to backpressure.

**Scenario `editors-1000.yaml`, fixed before the run:**
- 1,000 editors join over 60 s, then 5 minutes of measurement.
- Each editor has a viewport of about 1920×1080 at zoom 1, placed on the board with a hotspot distribution: 50% in one shared region, 50% spread across the board.
- Per editor: 1 op/s on average, split 60% drag updates, 20% creates, 15% text edits, and 5% deletes. Drags come in bursts at 10 Hz, 1–2 s long.
- Per editor: 15 Hz cursors while active.
- Board pre-seeded with 10k objects.

**Sweep:** 100, 250, 500, 1,000, 1,500, and 2,000 editors, plotting p99 against editor count. The report states the highest count where p99 < 100 ms. If 1,000 isn't reached, that is reported plainly along with the bottleneck found.

**Cheap rig (cost $0).** The primary rig is GitHub Actions, so load tests never compete with the demo or MilkRun for the Oracle free allowance (see ADR-0006).
- **Primary: GitHub Actions `ubuntu-latest`.** Standard runners for public repos are free, with 4 vCPU and 16 GB each.
  - Triggered by a `workflow_dispatch` workflow, `.github/workflows/bench.yml`.
  - The server stack and the loadgen run on the same runner, pinned to separate cores with Docker `--cpuset-cpus`:
    - Server (node plus Postgres): cores 0–1
    - Loadgen: cores 2–3
  - Anyone can reproduce the run from the repo.
  - Caveats:
    - Runner hardware varies between runs, so each run records `lscpu` and `free -m`, and each configuration runs 3 times. We report every run and the median p99.
    - Traffic goes over loopback, so the numbers exclude real network latency. That is stated with the results.
- **Secondary: over the internet.** A laptop runs about 100 editors against the public demo. This is a realistic end-to-end latency check, reported separately and labeled "WAN".
- **Not used:** Oracle VMs. The free A1 allowance is fully allocated to the demo and MilkRun.
- **Loadgen host limits:** file-descriptor limit ≥ 65k and a larger ephemeral port range. Loadgen CPU is recorded; if it exceeds 70%, the run is invalid, because the loadgen rather than the server would be the bottleneck.
- **Pre-check:** a local rehearsal with Docker, for debugging only. Its results are labeled "local".

### 2. Smooth with 100k objects, loading only the visible region
- **Seed:** `seed --objects 100000 --layout grid|clustered` (a mixed shape distribution).
- **Tool:** Playwright with Chrome, using a CDP performance trace.
- **Script:** load the board, pan at a fixed speed for 10 s, zoom from 100% to 5% (triggering LOD) and back, then pan in LOD mode.
- **Metrics:**
  - Time to first viewport render
  - Bytes downloaded before first render
  - Frame-time p50/p95/p99 and share of frames over 16.7 ms
  - Objects held in the client
  - JS heap size
- **"Smooth"** means frame-time p95 ≤ 16.7 ms (60 fps) on the stated hardware, reported alongside the laptop's CPU and GPU model. A 4× CPU-throttle run is also reported.
- **Server side:** viewport query latency (p99) on the 100k-object grid, measured with a Go benchmark.

### 3. Offline edits merge cleanly on reconnect
- **Property-based tests** (`rapid` in Go; `fast-check` for the TS merge code):
  - Random op streams across 3–10 replicas.
  - Random partitions, delays, duplicates, and reordering.
  - Assert that after healing, every replica equals the server state.
- **Shared test vectors:** the same vectors run through the Go and TS LWW implementations, which must produce identical states.
- **Integration test:** a real browser client goes offline (Playwright network emulation), makes N edits, while other clients edit the same objects. On reconnect, check that all converge and that the per-property winners match HLC order.
- **Reported:** number of generated cases and the seed of each run. There is no latency number here; the result is pass or fail plus the case count.

### 4. Storage for full history
- **Script:** `bench-storage` replays scripted edit streams of 10k, 100k, and 1M ops (the same mix as scenario 1) into a fresh board.
- **Measured:** `pg_total_relation_size` of `ops`, `op_segments`, and `snapshots`:
  - before compaction,
  - after compaction,
  - after snapshot retention.
- **Reported:** bytes per edit for each stage, plus the time to rebuild state at a random `seq` (p50/p99 over 1,000 random seeks) for the history slider.

### 5. Reconnect without losing edits, durable persistence
- **Chaos run:** during scenario 1 at 250 editors with 3 nodes, `docker kill` the owner node at T+120 s.
- **Metrics:**
  - **Lost edits:** the set of ops acknowledged to the loadgen, compared with the set persisted in Postgres after the run. Expected 0; any other value is a bug to fix.
  - **Duplicate edits:** expected 0.
  - **Time to recover:** from the kill until 99% of clients receive frames again.
  - Latency p99 during recovery.
- **Also run:** a paused owner (`docker pause` for 10 s, then unpause) to exercise fencing, checking that the stale owner steps down and no `seq` gaps or forks appear.

### 6. Synchronized timers
- 50 simulated clients get artificial clock offsets of ±5 min and jittered network delay (Linux `tc netem`, 20–150 ms).
- **Measured:** the spread (max − min) of displayed remaining time across clients, sampled every 100 ms. All clients live in one loadgen process, so they share a true clock.
- **Reported:** p50/p99 spread.

### 7. Real users (after launch)
- **Counts:** distinct guest ids that make at least one edit, per day and per week, plus boards created and boards with 2+ concurrent editors.
- **Collection:** counted server-side only, with no third-party analytics.
- **Published on:** the metrics panel and in the README.

## Load-test GIF for the README
A screen recording of the live metrics panel beside a board where 1,000+ bots are drawing, taken during run 1. It's converted to GIF with `ffmpeg` and `gifski`. The caption states the scenario and setup.
