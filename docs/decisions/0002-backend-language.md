# ADR-0002: Backend language and framework

- **Status:** Accepted
- **Date:** 2026-09-27

## Context
The server has to:
- hold 1,000+ WebSockets on one board,
- run a per-board actor that ticks every ~20 ms,
- encode frames for fan-out efficiently,
- ship as a small, self-hostable binary.

The project is solo and part-time, and it targets international backend roles. ADR-0001 removed the need to run Yjs on the server.

## Options
| Option | Pros | Cons |
|---|---|---|
| **Go** | Goroutine per connection and channels fit the actor model. Low memory per connection. Single static binary. Built-in profiling (pprof). Widely used in backend infrastructure. | Garbage-collection pauses need attention at high message rates. Less expressive type system. |
| **Rust (tokio + axum)** | Best raw performance, no GC. | Much slower to build part-time. |
| **Kotlin/Java (Netty)** | Mature, and common in enterprise job listings. | Heavier runtime on a free-tier VM. More boilerplate. |
| **Node/TypeScript** | Could share code with the frontend. | Single-threaded fan-out to 1,000 clients is a risk. Its main advantage, running Yjs natively, doesn't apply here. |

## Decision
**Go**, with these libraries:

| Concern | Library |
|---|---|
| WebSockets | `github.com/coder/websocket` |
| Postgres | `github.com/jackc/pgx/v5` |
| Wire format | Protobuf, generated with `buf` for both Go and TypeScript, so there is one schema |
| Metrics | `prometheus/client_golang` |
| Logging | `log/slog` |
| Tests | the standard `testing` package, `testcontainers-go` for Postgres, and `pgregory.net/rapid` for property-based tests |

Frontend: TypeScript, Vite, PixiJS v8, and Preact (see ADR-0003).

## Consequences
- The load generator (`loadgen/`) is also Go and reuses the protocol package.
- The wire format is binary. Debugging needs a small decoder tool, which is part of the scaffolding.
- GC tuning (`GOGC`, buffer pooling) is expected work during load testing.
