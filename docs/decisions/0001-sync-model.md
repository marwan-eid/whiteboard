# ADR-0001: Sync model

- **Status:** Accepted
- **Date:** 2026-09-27

## Context
Many editors change the same structured canvas at once, and some of them are offline. The design has to:
- converge deterministically,
- let the server send each client only its viewport (100k objects per board),
- store every edit as an event for history and replay,
- let the server validate and rate-limit edits.

## Options
| Option | Pros | Cons |
|---|---|---|
| **Yjs** | Mature. Offline merge and text CRDT built in. Large ecosystem. | Every client must load the whole document, so viewport-only loading isn't possible. The server sees opaque updates and can't validate, filter by viewport, or enforce per-object rules. Full history requires turning GC off, so the document grows without bound. Server-side decoding needs Node or Rust (yrs). |
| **Automerge** | Full history built in. Rust core. | Same whole-document model as Yjs. Heavier memory and CPU per operation at 100k objects. |
| **Custom: server-sequenced op log over a last-writer-wins (LWW) map CRDT** | The server understands objects, so it can do spatial interest filtering, validation, and permissions. The op log is the event store, so history and replay come for free. Works in any backend language. | We write and prove convergence ourselves. Text merges as a whole string, not per character. |

## Decision
**Use the custom model.**
- **Objects:** each object is a map of properties. Each property is an LWW register ordered by `(hlc, clientId)`, where `hlc` is a hybrid logical clock timestamp.
- **Why it converges:** merge is commutative, associative, and idempotent, so replicas converge whatever order the ops arrive in.
- **Offline merges:** an edit made offline two hours ago loses to a newer online edit of the same property, and merges with edits to *other* properties.
- **Server sequencing:** the server assigns each accepted op a per-board `seq`. That gives the event log a total order and makes replay trivial.
- **Clock abuse:** the server clamps client HLC values more than a small skew (for example 2 s) ahead of server time. A client with a clock set to the future can't win every conflict.
- **Deletes:** deleting sets an LWW `deleted` property to true. A concurrent edit to other properties does not bring the object back. Undo, restore, and history set `deleted = false` with a newer stamp. This keeps a single merge rule for everything.
- **Z-order:** a fractional-index string property, with ties broken by object id.
- **Undo:** local only. Undo issues inverse ops with fresh HLC timestamps.

Prior art: Figma's multiplayer design, which is server-authoritative with per-property LWW.

## Consequences
- **Tests:** convergence must be covered by property-based tests with random op orders, partitions, and duplicates.
- **Text in a sticky:** concurrent typing in the *same* sticky resolves to one winner. A per-object text CRDT is later scope.
- **Server per board:** the server must hold board state, so there is one actor per board, owned by one node (see ADR-0005).
- **Memory cost:** each object stores a timestamp per property. At about 12 properties × 16 B, that is roughly 200 B per object, or about 20 MB at 100k objects. Acceptable.
