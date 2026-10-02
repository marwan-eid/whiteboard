# Whiteboard

An open-source, self-hostable, real-time collaborative whiteboard.

**Live demo: https://51-170-191-178.sslip.io** (open it in two windows). It runs on Oracle Cloud's free tier, currently on a small fallback VM; see [docs/DEPLOY.md](docs/DEPLOY.md).

![150 simulated editors drawing on one board, with the live stats panel](docs/media/load-demo.gif)

*150 simulated editors ([cmd/loadgen](cmd/loadgen)) drawing on one board, seen from one browser with the live stats panel open. Recorded on one laptop running the server, the bots and the browser together, so this is a demo, not a benchmark. The measured numbers are in [benchmarks/results](benchmarks/results).*

> **Status: deployed (W10 of [the plan](docs/PLAN.md)); launch is next.** A working multiplayer whiteboard: boards sync live between browsers, persist in Postgres, survive a server crash, each client loads only the part of the board it is looking at, every version can be replayed and restored, boards can be private with revocable share links, edits made offline survive a reload, a board has a shared timer, dot voting and a live stats panel, and the Compose stack runs two server nodes: kill the one serving a board and its users move to the other without losing acknowledged edits. **Measured** on free GitHub Actions runners (loopback, one node on 2 cores): sync p99 21.8 ms at 100 editors and 58.4 ms at 500 on one board. The 1,000-editor target (p99 < 100 ms) was **not** reached: 231 ms at 1,000 editors ([results](benchmarks/results/2026-10-02-sync.md)). A 100k-object board pans at p95 under 7 ms on a laptop GPU ([results](benchmarks/results/2026-10-02-browser-100k.md)).

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
| Shared timer | **⏱ Timer** at the top; everyone sees the same countdown |
| Dot vote | **🗳 Vote** to start; select shapes and **Vote for selection**; **End vote** shows the results |
| Live server stats | **📊** at the bottom left |

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
| Simulated editors (reports sync latency) | `go run ./cmd/loadgen -editors 100 -duration 30s`; above 64 editors from one machine, start the stack with `MAX_CONNS_PER_IP=0` |
| Fill a board with objects | `go run ./cmd/seed -board big -objects 100000` |
| Browser performance on a big board | `npx -w web playwright test -c perf.config.ts` |
| Storage per edit (wipes the given database) | `go run ./cmd/storagebench -db <scratch postgres url> -edits 100000` |

## Layout

```
cmd/whiteboard     node binary
cmd/loadgen        load generator
internal/          Go packages: board (actors), cluster (leases, routing), gateway, store, access, doc, client, ...
test/e2e/          Go tests against the Compose stack (SIGKILL failover)
proto/             wire protocol (Protobuf), source of truth for Go and TS
web/               browser client (TypeScript, PixiJS, Preact)
deploy/            Dockerfiles and Caddyfile
docs/              architecture, plan, benchmarks, decisions (ADRs)
```

## Docs

- [Architecture](docs/ARCHITECTURE.md)
- [Plan](docs/PLAN.md)
- [Benchmarks: methodology and results](docs/BENCHMARKS.md)
- [Deploying and self-hosting](docs/DEPLOY.md)
- [Write-up: how it works and what was measured](docs/WRITEUP.md)
- [Launch notes](docs/LAUNCH.md)
- [Decisions](docs/decisions/)
