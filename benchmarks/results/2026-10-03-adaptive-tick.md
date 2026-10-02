# Benchmark 1, follow-up: an adaptive tick for busy boards

**Result: on the same runners, a 50 ms tick for very busy boards lowered the 1,000-editor p99 in all 4 pairs, from a median of 214.7 ms to 189.4 ms (−12%), and node CPU by about 30%. The 1,000-editor target (p99 < 100 ms) is still not reached.** At 500 editors the slower tick cost 2.6–6.8 ms of p99 in every pair, so the shipped setting leaves boards of up to 500 clients at 20 ms.

**Correction:** the earlier 122.8 ms at 1,000 editors with a 50 ms tick ([2026-10-02-sync.md](2026-10-02-sync.md)) did not replicate. Measured in pairs on the same runners, a 50 ms tick gives 167.8–191.5 ms. The earlier runs were compared across different runners, and runners differ a lot, as shown below.

Method: [docs/BENCHMARKS.md](../../docs/BENCHMARKS.md), scenario 1, with the setup of [2026-10-02-sync.md](2026-10-02-sync.md) (GitHub Actions, one node plus Postgres on 2 cores, loadgen on the other 2, loopback). Raw files: [2026-10-03-adaptive-tick/](2026-10-03-adaptive-tick/).

## The change

`board.Config.TickMax` and `BusyFrom` (commit `cde0f54`; setting `TICK_MAX`): a board with more than `BusyFrom` clients ticks more slowly, linearly up to `TickMax` (50 ms) at twice `BusyFrom`. Small boards keep the 20 ms tick. A longer tick means fewer, larger frames per client, so fewer socket writes, which profiling had shown to be the largest cost at 1,000 editors.

## First try: runners, not ticks (commit `cde0f54`)

The first sweep compared the adaptive tick, which then started at 400 clients, against the earlier 20 ms runs. It looked worse at both 500 editors (median p99 213.4 ms) and 1,000 (294.4 ms). The node's metrics showed why: in 4 of 6 runs the **group commit took 100–200 ms at p99**, against 3–25 ms in the others. The runner's disk was slow (an fsync waits for it), and every run with slow commits had a high sync p99:

| Run | Sync p99 | Commit p99 |
|---|---|---|
| 500 editors, run 1 | 36.7 ms | ≤ 6.4 ms |
| 500, run 2 | 234.4 ms | ≤ 204.8 ms |
| 500, run 3 | 213.4 ms | ≤ 102.4 ms |
| 1,000, run 1 | 294.4 ms | ≤ 204.8 ms |
| 1,000, run 2 | 176.0 ms | ≤ 25.6 ms |
| 1,000, run 3 | 393.0 ms | ≤ 204.8 ms |

Run 500-2 was already at 473 ms p99 with only 84 editors connected, so the tick was not the cause. Comparing variants across runners had measured the disks. Two changes followed (commit `a12a890`):
- [bench.yml](../../.github/workflows/bench.yml) takes several `tick_max` values and runs **each on the same runner**, one after the other, with a fresh server and database each time. The order alternates by run number.
- `benchsum` reports each run's commit p99 (upper bound of its histogram bucket).

## Paired runs (commit `a12a890`, 4 runners per editor count)

`tick_max` 20 ms is the old fixed tick (adaptive off); 50 ms is the adaptive tick, which at 1,000 clients is a 50 ms tick and at 500 clients was 27.5 ms (it then started at 400).

| Editors | Run | p99, 20 ms | p99, adaptive | p50, 20 ms | p50, adaptive | Node CPU, 20 ms | Node CPU, adaptive |
|---|---|---|---|---|---|---|---|
| 500 | 1 | 43.2 | 46.1 | 15.9 | 20.5 | 60% | 53% |
| 500 | 2 | 36.5 | 39.1 | 14.6 | 18.9 | 46% | 39% |
| 500 | 3 | 40.0 | 46.8 | 15.3 | 19.8 | 45% | 39% |
| 500 | 4 | 45.9 | 50.4 | 16.4 | 21.1 | 62% | 53% |
| 1,000 | 1 | 222.3 | 178.6 | 32.1 | 48.2 | 123% | 88% |
| 1,000 | 2 | 179.8 | 167.8 | 26.5 | 45.8 | 108% | 73% |
| 1,000 | 3 | 213.2 | 191.5 | 32.8 | 48.3 | 125% | 85% |
| 1,000 | 4 | 214.7 | 189.4 | 33.4 | 49.4 | 127% | 89% |

All times in ms; 100% CPU is one of the node's 2 cores. Medians: at 500 editors, 43.2 → 46.8 ms; at 1,000, 214.7 → 189.4 ms. Commit p99 was at most 25.6 ms in every run, and no run was invalid. Full tables: [paired-a12a890/summary.md](2026-10-03-adaptive-tick/paired-a12a890/summary.md).

**Reading it:**
- **At 1,000 editors the slower tick helps the tail and costs the median.** p99 fell in every pair; p50 rose by 15–19 ms, about the extra wait for the next tick.
- **Part of the gain may be the loadgen's.** With fewer frames to read, its CPU fell from 59–66% to 47–53% of its two cores. Both are under the 70% validity limit, but a busier loadgen adds some delay to what it measures.
- **At 500 editors the 20 ms tick is better**, and it already meets the target with a wide margin. So the shipped default (`BusyFrom` = 500) starts slowing only above 500 clients and reaches 50 ms at 1,000. Between 500 and 1,000 clients that setting is interpolated, not measured.
- **The 20 ms baseline here (median 214.7 ms) is close to the earlier 231.4 ms**, from different runners.

## Why the target is still out of reach on this rig

The node's floor at a 50 ms tick is about one tick plus the commit, and in this scenario half the editors share one view, so each edit goes to about 500 sockets. What remains, from [2026-10-02-sync.md](2026-10-02-sync.md): batching several frames into one write per client, and fanning out through edge nodes so one node does not write to every socket.
