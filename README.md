# Whiteboard

An open-source, self-hostable, real-time collaborative whiteboard.

> **Status: early development. W2 (durability) of [the plan](docs/PLAN.md) is done.** Boards sync live between browsers and persist in Postgres; edits survive a server crash. No performance numbers have been measured yet. Targets are in [docs/BENCHMARKS.md](docs/BENCHMARKS.md).

## Run it

Requires Docker.

```sh
docker compose up --build
```

Then open http://localhost:8080 in two browser windows. Open `/b/<any-id>` to join a specific board.

- Double-click empty space to add a shape.
- Drag a shape to move it.
- Select a shape and press Delete to remove it.

## Develop

Requires Go 1.25+ and Node 24+. For the database, either run Docker or point `DATABASE_URL` at any Postgres 17.

```sh
npm install                          # web + codegen tooling
docker compose up -d postgres        # database only

# terminal 1: a node on :8081
DATABASE_URL=postgres://whiteboard:whiteboard@localhost:5432/whiteboard?sslmode=disable go run ./cmd/whiteboard

# terminal 2: web dev server on :5173 (proxies /ws to the node)
npm run dev:web
```

| Task | Command |
|---|---|
| Go tests (integration tests need Docker; they're skipped without it) | `go test ./...` |
| Go lint | `golangci-lint run` |
| Web lint / typecheck / unit tests | `npm run lint`, `npm run typecheck`, `npm test` |
| Browser smoke test (stack must be running) | `npm -w web run e2e` |
| Regenerate protocol code after editing `proto/` | `npm run gen` |
| Simulated clients | `go run ./cmd/loadgen -clients 100 -duration 10s` |

## Layout

```
cmd/whiteboard     node binary
cmd/loadgen        load generator
internal/          Go packages (gateway, db, config, metrics, protocol, generated pb)
proto/             wire protocol (Protobuf), source of truth for Go and TS
web/               browser client (TypeScript, PixiJS, Preact)
deploy/            Dockerfiles and Caddyfile
docs/              architecture, plan, benchmarks, decisions (ADRs)
```

## Docs

- [Architecture](docs/ARCHITECTURE.md)
- [Plan](docs/PLAN.md)
- [Benchmarks methodology](docs/BENCHMARKS.md)
- [Decisions](docs/decisions/)
