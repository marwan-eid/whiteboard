# Benchmark 6: how closely clients agree on the timer

**Local run, one laptop (Intel Core i7-8750H, Windows 11, Docker Desktop stack with two nodes).** Method: [docs/BENCHMARKS.md](../../docs/BENCHMARKS.md), target 6, with one change, described below. Tool: [cmd/timerskew](../../cmd/timerskew).

## Method

1. A 10-minute timer is started on a board.
2. 50 simulated clients connect.
3. Each client's local clock is off by a random amount, up to **±5 minutes**.
4. Each message is delayed by a random **10–75 ms each way**, independently. A round trip is therefore 20–150 ms, and the two directions can differ by up to 65 ms.
5. Each client estimates the server clock the way the web client does ([web/src/net/clock.ts](../../web/src/net/clock.ts)): from pings every 500 ms, it keeps the sample with the lowest round-trip time of the last 8.
6. Each client computes the remaining time it would display, `ends_at − (local clock + estimated offset)`.
7. Every 100 ms the tool records the **spread**: the largest displayed value minus the smallest, across all 50 clients.

**Change from the plan:** the plan used `tc netem` for delay. Here the tool adds delay inside its own process, which runs on any OS and makes the asymmetry explicit.

## Results

| Run | Delay each way | Samples | Spread p50 | Spread p99 | Max |
|---|---|---|---|---|---|
| Jittered | 10–75 ms, random | 200 | 42.0 ms | 50.5 ms | 50.5 ms |
| Control | 30 ms, fixed | 100 | 2.9 ms | 3.9 ms | 3.9 ms |

**Reading it:**
- **Clocks that are minutes off don't matter.** All clients agree to within about 50 ms.
- **The remaining spread comes from asymmetric delay.** An offset estimated from round trips assumes the trip out and the trip back take the same time; when they don't, each client's estimate is off by half the difference. With symmetric delay (the control run), the spread drops to about 3 ms.
- **For comparison:** a countdown shows whole seconds, so a 50 ms spread is invisible except right at a second boundary.
