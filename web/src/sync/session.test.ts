import { create, equals } from "@bufbuild/protobuf";
import fc from "fast-check";
import { describe, expect, it } from "vitest";
import {
  AckSchema,
  FrameSchema,
  ObjectStateSchema,
  OpSchema,
  SequencedBatchSchema,
  ShapeType,
  WelcomeSchema,
  type Frame,
  type ObjectProps,
  type Op,
  type OpBatch,
  type Welcome,
} from "../gen/whiteboard/v1/protocol_pb";
import { Doc } from "./doc";
import { SyncSession } from "./session";
import { stampFromProto, stampToProto } from "./stamp";

/**
 * A minimal in-memory model of the board actor (internal/board): it
 * sequences batches, dedupes resends by client_seq, acks the sender and
 * forwards to everyone else. The network between it and each client is a
 * pair of queues the test delivers from in random order, and can cut.
 */
class ModelServer {
  doc = new Doc();
  seq = 0;
  lastClientSeq = new Map<number, number>();
  clients = new Map<number, ModelClient>();

  join(c: ModelClient): void {
    this.clients.set(c.id, c);
    c.inbox.push(
      create(WelcomeSchema, {
        seq: BigInt(this.seq),
        objects: this.doc.snapshot(),
        lastClientSeq: BigInt(this.lastClientSeq.get(c.id) ?? 0),
      }),
    );
  }

  leave(c: ModelClient): void {
    if (this.clients.get(c.id) === c) this.clients.delete(c.id);
  }

  receive(c: ModelClient, b: OpBatch): void {
    const cs = Number(b.clientSeq);
    const last = this.lastClientSeq.get(c.id) ?? 0;
    if (cs <= last) {
      c.inbox.push(create(FrameSchema, { acks: [create(AckSchema, { clientSeq: b.clientSeq })] }));
      return;
    }
    if (cs !== last + 1) throw new Error(`client ${c.id} skipped from ${last} to ${cs}`);
    this.lastClientSeq.set(c.id, cs);
    const stamp = { ...stampFromProto(b.stamp), clientId: c.id };
    this.doc.applyBatch(b.ops, stamp);
    this.seq++;
    const sb = create(SequencedBatchSchema, { seq: BigInt(this.seq), stamp: stampToProto(stamp), ops: b.ops });
    for (const other of this.clients.values()) {
      if (other !== c) other.inbox.push(create(FrameSchema, { batches: [sb] }));
    }
    c.inbox.push(
      create(FrameSchema, { acks: [create(AckSchema, { clientSeq: b.clientSeq, seq: BigInt(this.seq), stamp: stampToProto(stamp) })] }),
    );
  }
}

class ModelClient {
  readonly session: SyncSession;
  inbox: (Welcome | Frame)[] = [];
  outbox: OpBatch[] = [];
  connected = false;
  welcomed = false;

  constructor(
    readonly id: number,
    private readonly server: ModelServer,
    now: () => number,
  ) {
    this.session = new SyncSession({
      clientId: id,
      now,
      send: (b) => {
        if (!this.welcomed) return false;
        this.outbox.push(b);
        return true;
      },
      resync: () => {
        this.disconnect();
        this.connect();
      },
    });
  }

  connect(): void {
    if (this.connected) return;
    this.connected = true;
    this.server.join(this);
  }

  /** Cuts the connection; anything in flight either way is lost. */
  disconnect(): void {
    this.connected = this.welcomed = false;
    this.inbox = [];
    this.outbox = [];
    this.server.leave(this);
  }

  deliverIn(): void {
    const m = this.inbox.shift();
    if (!m) return;
    if (m.$typeName === "whiteboard.v1.Welcome") {
      this.welcomed = true;
      this.session.onWelcome(m);
    } else {
      this.session.onFrame(m);
    }
  }

  deliverOut(): void {
    const b = this.outbox.shift();
    if (b) this.server.receive(this, b);
  }
}

function snapshotsEqual(a: Doc, b: Doc): boolean {
  const sa = a.snapshot();
  const sb = b.snapshot();
  return sa.length === sb.length && sa.every((s, i) => equals(ObjectStateSchema, s, sb[i]!));
}

type Action =
  | { kind: "edit"; client: number; target: number; props: Partial<ObjectProps> }
  | { kind: "create"; client: number }
  | { kind: "in" | "out" | "disconnect" | "connect"; client: number }
  | { kind: "tick" };

const N = 3;
const clientArb = fc.integer({ min: 0, max: N - 1 });
const actionArb: fc.Arbitrary<Action> = fc.oneof(
  { weight: 5, arbitrary: fc.record({ kind: fc.constant("in" as const), client: clientArb }) },
  { weight: 5, arbitrary: fc.record({ kind: fc.constant("out" as const), client: clientArb }) },
  {
    weight: 4,
    arbitrary: fc.record({
      kind: fc.constant("edit" as const),
      client: clientArb,
      target: fc.nat(),
      props: fc.oneof(
        fc.record({ x: fc.integer({ min: 0, max: 100 }) }),
        fc.record({ fill: fc.integer({ min: 0, max: 0xffff_ffff }) }),
        fc.record({ deleted: fc.boolean() }),
      ),
    }),
  },
  { weight: 1, arbitrary: fc.record({ kind: fc.constant("create" as const), client: clientArb }) },
  { weight: 1, arbitrary: fc.record({ kind: fc.constant("disconnect" as const), client: clientArb }) },
  { weight: 2, arbitrary: fc.record({ kind: fc.constant("connect" as const), client: clientArb }) },
  { weight: 2, arbitrary: fc.constant({ kind: "tick" as const }) },
);

function run(actions: Action[]): { server: ModelServer; clients: ModelClient[] } {
  let time = 1_000;
  const server = new ModelServer();
  const clients = Array.from({ length: N }, (_, i) => new ModelClient(i + 1, server, () => time));
  const known: string[] = [];

  for (const c of clients) c.connect();
  // Every client starts with one object so edits have targets.
  for (const c of clients) {
    while (c.inbox.length) c.deliverIn();
    const id = c.session.newObjectId();
    known.push(id);
    c.session.edit([create(OpSchema, { id, props: { type: ShapeType.RECT, x: 0 } })]);
  }

  for (const a of actions) {
    if (a.kind === "tick") {
      time += 1;
      continue;
    }
    const c = clients[a.client]!;
    switch (a.kind) {
      case "in":
        c.deliverIn();
        break;
      case "out":
        c.deliverOut();
        break;
      case "disconnect":
        c.disconnect();
        break;
      case "connect":
        c.connect();
        break;
      case "create": {
        const id = c.session.newObjectId();
        known.push(id);
        c.session.edit([create(OpSchema, { id, props: { type: ShapeType.ELLIPSE, x: 1 } })]);
        break;
      }
      case "edit": {
        // Only edit objects this replica has seen created, as a real UI would.
        const visible = known.filter((id) => c.session.doc.get(id)?.props.type !== undefined);
        if (visible.length === 0) break;
        const op: Op = create(OpSchema, { id: visible[a.target % visible.length]!, props: a.props });
        c.session.edit([op]);
        break;
      }
    }
  }

  // Heal: everyone reconnects and the network drains.
  for (const c of clients) c.connect();
  for (let guard = 0; clients.some((c) => c.inbox.length || c.outbox.length); guard++) {
    if (guard > 100_000) throw new Error("network did not drain");
    for (const c of clients) {
      c.deliverOut();
      c.deliverIn();
    }
  }
  return { server, clients };
}

describe("SyncSession against a model server", () => {
  it("converges after random delivery, disconnects and reconnects", () => {
    fc.assert(
      fc.property(fc.array(actionArb, { maxLength: 150 }), (actions) => {
        const { server, clients } = run(actions);
        for (const c of clients) {
          expect(c.session.pendingCount, `client ${c.id} pending`).toBe(0);
          expect(c.session.seq, `client ${c.id} seq`).toBe(server.seq);
          expect(snapshotsEqual(c.session.doc, server.doc), `client ${c.id} replica`).toBe(true);
        }
      }),
      { numRuns: 300 },
    );
  });

  it("shows local edits immediately and counts them as pending until acked", () => {
    const server = new ModelServer();
    const c = new ModelClient(1, server, () => 5);
    c.connect();
    c.deliverIn();
    const changes: (ReadonlySet<string> | null)[] = [];
    c.session.subscribe((ch) => changes.push(ch));

    const id = c.session.newObjectId();
    c.session.edit([create(OpSchema, { id, props: { type: ShapeType.RECT, x: 3 } })]);
    expect(c.session.doc.get(id)?.props.x).toBe(3);
    expect(c.session.pendingCount).toBe(1);
    expect(changes.at(-1)).toEqual(new Set([id]));

    c.deliverOut();
    c.deliverIn();
    expect(c.session.pendingCount).toBe(0);
    expect(c.session.seq).toBe(1);
  });

  it("resyncs when the server rewrote its stamp", () => {
    let resyncs = 0;
    const s = new SyncSession({ clientId: 1, now: () => 10, send: () => true, resync: () => resyncs++ });
    s.onWelcome(create(WelcomeSchema, {}));
    s.edit([create(OpSchema, { id: "1:1", props: { type: ShapeType.RECT } })]);
    s.onFrame(
      create(FrameSchema, {
        acks: [create(AckSchema, { clientSeq: 1n, seq: 1n, stamp: stampToProto({ wallMs: 5, counter: 0, clientId: 1 }) })],
      }),
    );
    expect(resyncs).toBe(1);
  });
});
