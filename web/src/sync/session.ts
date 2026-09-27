import { create } from "@bufbuild/protobuf";
import { OpBatchSchema, type Frame, type Op, type OpBatch, type Welcome } from "../gen/whiteboard/v1/protocol_pb";
import { Doc } from "./doc";
import { HybridClock, stampFromProto, stampToProto, type Stamp } from "./stamp";

export interface SessionOptions {
  clientId: number;
  /** Server-synchronized wall clock in ms (Connection.serverNow). */
  now: () => number;
  /** Sends a batch if connected and welcomed; false leaves it queued. */
  send: (batch: OpBatch) => boolean;
  /** Reconnects to get a fresh snapshot. */
  resync: () => void;
}

/** null means "everything may have changed" (a new snapshot). */
export type ChangeListener = (changed: ReadonlySet<string> | null) => void;

interface Pending {
  batch: OpBatch;
  stamp: Stamp;
}

/**
 * The client side of board sync; mirrors internal/client (Go).
 *
 * Edits apply to the local replica at once and wait in a pending queue until
 * the server acks them. Because merge is per-property last-writer-wins,
 * remote batches can be applied as they arrive without rebasing local ones.
 * On reconnect the server's snapshot replaces the replica and unacked edits
 * are reapplied on top and resent; the server skips any it already applied.
 */
export class SyncSession {
  doc = new Doc();
  readonly clientId: number;
  private readonly clock: HybridClock;
  private readonly listeners = new Set<ChangeListener>();
  private readonly idPrefix: string;
  private pending: Pending[] = [];
  private nextClientSeq = 1;
  private nextObject = 0;
  private serverSeq = 0;
  /** What the server believes we hold: the objects in our viewport. Only the
   * server changes it (Welcome, Frame.objects, Frame.leave). */
  private held = new Set<string>();

  constructor(private readonly opts: SessionOptions) {
    this.clientId = opts.clientId;
    this.clock = new HybridClock(opts.clientId, opts.now);
    this.idPrefix = `${opts.clientId.toString(36)}:`;
  }

  get pendingCount(): number {
    return this.pending.length;
  }

  get seq(): number {
    return this.serverSeq;
  }

  subscribe(fn: ChangeListener): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  /** An object id no other client can generate. */
  newObjectId(): string {
    return this.idPrefix + (++this.nextObject).toString(36);
  }

  /** Applies ops locally now and queues them for the server. */
  edit(ops: Op[]): void {
    if (ops.length === 0) return;
    const stamp = this.clock.now();
    const batch = create(OpBatchSchema, { clientSeq: BigInt(this.nextClientSeq++), stamp: stampToProto(stamp), ops });
    const changed = this.doc.applyBatch(ops, stamp);
    this.pending.push({ batch, stamp });
    this.opts.send(batch);
    this.emit(changed);
  }

  onWelcome(w: Welcome): void {
    const lastApplied = Number(w.lastClientSeq);
    this.doc = Doc.fromSnapshot(w.objects);
    this.serverSeq = Number(w.seq);
    this.held = new Set(w.objects.map((o) => o.id));
    this.pending = this.pending.filter((p) => Number(p.batch.clientSeq) > lastApplied);
    for (const p of this.pending) this.doc.applyBatch(p.batch.ops, p.stamp);
    for (const o of w.objects) for (const fs of o.stamps) this.clock.observe(stampFromProto(fs.stamp));
    for (const p of this.pending) this.opts.send(p.batch);
    this.emit(null);
  }

  onFrame(f: Frame): void {
    const changed = new Set<string>();
    let maxSeq = Math.max(this.serverSeq, Number(f.seq));
    for (const b of f.batches) {
      const seq = Number(b.seq);
      if (seq <= this.serverSeq) continue;
      const st = stampFromProto(b.stamp);
      // Ops only apply to objects we hold; the server sends objects that come
      // into view in full (f.objects).
      for (const op of b.ops) if (this.held.has(op.id) && this.doc.apply(op, st)) changed.add(op.id);
      this.clock.observe(st);
      maxSeq = Math.max(maxSeq, seq);
    }
    // A left object with pending local edits stays until they are acked: the
    // server never echoes our own edits, so dropping it would lose them.
    const busy = this.pendingIds();
    for (const id of f.leave) {
      this.held.delete(id);
      if (!busy.has(id) && this.doc.delete(id)) changed.add(id);
    }
    for (const s of f.objects) {
      this.doc.mergeState(s);
      this.held.add(s.id);
      changed.add(s.id);
      for (const fs of s.stamps) this.clock.observe(stampFromProto(fs.stamp));
    }

    const acked = new Set<string>();

    let resync = false;
    for (const a of f.acks) {
      const i = this.pending.findIndex((p) => p.batch.clientSeq === a.clientSeq);
      if (i < 0) continue;
      const [sent] = this.pending.splice(i, 1);
      for (const op of sent!.batch.ops) acked.add(op.id);
      if (a.rejected) {
        resync = true;
      } else if (a.seq !== 0n) {
        // The server keeps our stamp unless it was too far in the future; if
        // it replaced it, our replica holds values under a stamp nobody else has.
        const applied = stampFromProto(a.stamp);
        if (applied.wallMs !== sent!.stamp.wallMs || applied.counter !== sent!.stamp.counter) resync = true;
        maxSeq = Math.max(maxSeq, Number(a.seq));
      }
    }
    this.serverSeq = maxSeq;
    // Objects we edited but do not hold (say, edits to something the server
    // has since deleted) go once nothing about them is pending.
    const stillBusy = this.pendingIds();
    for (const id of acked) {
      if (!this.held.has(id) && !stillBusy.has(id) && this.doc.delete(id)) changed.add(id);
    }
    if (resync) {
      this.opts.resync();
      return;
    }
    this.emit(changed);
  }

  /** Objects with unacknowledged local edits. */
  private pendingIds(): Set<string> {
    const ids = new Set<string>();
    for (const p of this.pending) for (const op of p.batch.ops) ids.add(op.id);
    return ids;
  }

  // Notifies even when no object changed: the pending count may have.
  private emit(changed: ReadonlySet<string> | null): void {
    for (const fn of this.listeners) fn(changed);
  }
}
