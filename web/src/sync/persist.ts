import { fromBinary, toBinary } from "@bufbuild/protobuf";
import { OpBatchSchema, type OpBatch } from "../gen/whiteboard/v1/protocol_pb";
import { randomClientId } from "../net/protocol";

/**
 * Keeps a tab's unsynced edits in IndexedDB so they survive a reload, a
 * crash or closing the tab while offline.
 *
 * Each tab works under its own client id per board (the server allows one
 * live connection per id). The id is held with a Web Lock for the life of the
 * tab, and ids are reused: a tab that loads later takes over an unheld id and
 * resends its pending batches. Resending is safe because the server skips
 * batches it already applied (by client id and client seq).
 */

/** What the session writes as it goes. Writes are batched and asynchronous. */
export interface SessionStore {
  saveBatch(batch: OpBatch): void;
  dropBatch(clientSeq: bigint): void;
  saveCounters(nextClientSeq: number, nextObject: number): void;
}

/** A client identity's saved state. */
export interface Restored {
  nextClientSeq: number;
  nextObject: number;
  /** Unacknowledged batches, in client seq order. */
  pending: OpBatch[];
}

export interface Identity {
  clientId: number;
  restored: Restored | null;
  store: SessionStore | null;
}

const DB_NAME = "whiteboard";
const IDS = "identities"; // key [boardId, clientId] -> { boardId, clientId, nextClientSeq, nextObject }
const BATCHES = "batches"; // key [boardId, clientId, clientSeq] -> Uint8Array (OpBatch)

interface IdentityRecord {
  boardId: string;
  clientId: number;
  nextClientSeq: number;
  nextObject: number;
}

/**
 * Takes a client id for this tab on boardId, with its saved state. Without
 * IndexedDB or Web Locks (old browsers, plain-HTTP origins other than
 * localhost) it returns a fresh id and edits live in memory only.
 */
export async function acquireIdentity(boardId: string): Promise<Identity> {
  const fresh: Identity = { clientId: randomClientId(), restored: null, store: null };
  if (!("locks" in navigator) || typeof indexedDB === "undefined") return fresh;
  try {
    const db = await openDb();
    const saved = await request(
      db.transaction(IDS).objectStore(IDS).getAll(IDBKeyRange.bound([boardId], [boardId, []])) as IDBRequest<IdentityRecord[]>,
    );
    for (const rec of saved) {
      if (await holdLock(boardId, rec.clientId)) {
        const pending = await loadBatches(db, boardId, rec.clientId);
        return {
          clientId: rec.clientId,
          restored: { nextClientSeq: rec.nextClientSeq, nextObject: rec.nextObject, pending },
          store: new IdbStore(db, boardId, rec.clientId),
        };
      }
    }
    // Every saved id is in use by another tab: start a new one.
    if (!(await holdLock(boardId, fresh.clientId))) return fresh;
    const store = new IdbStore(db, boardId, fresh.clientId);
    store.saveCounters(1, 0);
    return { ...fresh, store };
  } catch (err) {
    console.warn("offline storage unavailable; unsynced edits will not survive a reload", err);
    return fresh;
  }
}

function openDb(): Promise<IDBDatabase> {
  const req = indexedDB.open(DB_NAME, 1);
  req.onupgradeneeded = () => {
    req.result.createObjectStore(IDS, { keyPath: ["boardId", "clientId"] });
    req.result.createObjectStore(BATCHES);
  };
  return request(req);
}

function request<T>(req: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error ?? new Error("IndexedDB request failed"));
  });
}

/** Resolves true once this tab holds the id's lock (kept until the tab goes away), false if another tab has it. */
function holdLock(boardId: string, clientId: number): Promise<boolean> {
  return new Promise((resolve, reject) => {
    navigator.locks
      .request(`whiteboard:${boardId}:${clientId}`, { ifAvailable: true }, (lock) => {
        resolve(lock !== null);
        // Holding the returned promise open keeps the lock for the page's lifetime.
        return lock ? new Promise<never>(() => {}) : undefined;
      })
      .catch(reject);
  });
}

async function loadBatches(db: IDBDatabase, boardId: string, clientId: number): Promise<OpBatch[]> {
  const range = IDBKeyRange.bound([boardId, clientId, 0], [boardId, clientId, Infinity]);
  const rows = await request(db.transaction(BATCHES).objectStore(BATCHES).getAll(range) as IDBRequest<Uint8Array[]>);
  return rows.map((bytes) => fromBinary(OpBatchSchema, bytes)); // keys sort by client seq
}

/**
 * Collects writes and commits them together a moment later: a pointer drag
 * produces a batch per event, and one transaction per event would be wasteful.
 * Pending writes are flushed when the page is hidden.
 */
class IdbStore implements SessionStore {
  private puts = new Map<number, Uint8Array>();
  private drops = new Set<number>();
  private counters: [number, number] | null = null;
  private timer: ReturnType<typeof setTimeout> | undefined;

  constructor(
    private readonly db: IDBDatabase,
    private readonly boardId: string,
    private readonly clientId: number,
  ) {
    addEventListener("pagehide", () => this.flush());
    addEventListener("visibilitychange", () => {
      if (document.visibilityState === "hidden") this.flush();
    });
  }

  saveBatch(batch: OpBatch): void {
    const seq = Number(batch.clientSeq);
    this.drops.delete(seq);
    this.puts.set(seq, toBinary(OpBatchSchema, batch));
    this.schedule();
  }

  dropBatch(clientSeq: bigint): void {
    const seq = Number(clientSeq);
    // Acked before it was ever written: nothing to store or delete.
    if (!this.puts.delete(seq)) this.drops.add(seq);
    this.schedule();
  }

  saveCounters(nextClientSeq: number, nextObject: number): void {
    this.counters = [nextClientSeq, nextObject];
    this.schedule();
  }

  private schedule(): void {
    this.timer ??= setTimeout(() => this.flush(), 100);
  }

  private flush(): void {
    clearTimeout(this.timer);
    this.timer = undefined;
    if (this.puts.size === 0 && this.drops.size === 0 && !this.counters) return;
    const tx = this.db.transaction([IDS, BATCHES], "readwrite");
    const batches = tx.objectStore(BATCHES);
    for (const [seq, bytes] of this.puts) batches.put(bytes, [this.boardId, this.clientId, seq]);
    for (const seq of this.drops) batches.delete([this.boardId, this.clientId, seq]);
    if (this.counters) {
      const [nextClientSeq, nextObject] = this.counters;
      tx.objectStore(IDS).put({ boardId: this.boardId, clientId: this.clientId, nextClientSeq, nextObject } satisfies IdentityRecord);
    }
    tx.onerror = () => console.warn("saving unsynced edits failed", tx.error);
    // Commit now rather than when the event loop idles: during pagehide there may be no later.
    tx.commit();
    this.puts = new Map();
    this.drops = new Set();
    this.counters = null;
  }
}
