# Whiteboard

An open-source, self-hostable, real-time collaborative whiteboard.

> **Status: early development. W5 (history) of [the plan](docs/PLAN.md) is done.** A working multiplayer whiteboard: boards sync live between browsers, persist in Postgres, survive a server crash, each client loads only the part of the board it is looking at, and every version can be replayed and restored. The performance targets in [docs/BENCHMARKS.md](docs/BENCHMARKS.md) have not been benchmarked yet; local pre-checks are in [benchmarks/results](benchmarks/results).

## Run it

Requires Docker.

```sh
docker compose up --build
```

Then open http://localhost:8080 in two browser windows. Open `/b/<any-id>` to join a specific board; anyone with the id can edit it. For a private board, use **Boards → New private board**, then **Share** to make view or edit links (revocable).

For anything reachable by others, set your own `SECRET` (it signs guest identities): `SECRET=$(openssl rand -hex 32) docker compose up --build`.

| Action | How |
|---|---|
| Tools | Toolbar, or keys: V select, H hand, R rectangle, O ellipse, S sticky note, T text, A arrow, P pen |
| Add a rectangle quickly | Double-click empty space |
| Edit text | Double-click a sticky note or text, or press Enter; Esc or Ctrl+Enter to finish |
| Select several | Shift+click, or drag a box on empty space; Ctrl+A for all |
| Move, resize, delete | Drag; drag a handle; Delete |
| Attach an arrow | Start or end it on a shape; it follows the shape |
| Undo, redo | Ctrl+Z, Ctrl+Shift+Z |
| Pan, zoom | Scroll, or Space+drag, or the hand tool; Ctrl+scroll or pinch to zoom |

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
| Simulated editors (reports sync latency) | `go run ./cmd/loadgen -editors 100 -duration 30s` |
| Fill a board with objects | `go run ./cmd/seed -board big -objects 100000` |
| Browser performance on a big board | `npx -w web playwright test -c perf.config.ts` |
| Storage per edit (wipes the given database) | `go run ./cmd/storagebench -db <scratch postgres url> -edits 100000` |

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
