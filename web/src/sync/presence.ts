import type { CursorUpdate } from "../gen/whiteboard/v1/protocol_pb";

export interface Point {
  x: number;
  y: number;
}

const PALETTE = [0xe5484d, 0x2f81f7, 0x2da44e, 0xbf8700, 0x8250df, 0xdb61a2, 0x1b7c83, 0xd4600e];

/** A stable color per client, so the same person looks the same to everyone. */
export function cursorColor(clientId: number): number {
  return PALETTE[clientId % PALETTE.length]!;
}

export function cursorName(clientId: number): string {
  return `Guest ${clientId.toString(36).slice(-4).toUpperCase()}`;
}

/**
 * Live cursors: throttles our own pointer to intervalMs (always sending the
 * latest position last) and tracks everyone else's.
 */
export class Presence {
  readonly others = new Map<number, Point>();
  private readonly listeners = new Set<() => void>();
  private lastSent = -Infinity;
  private pending: Point | null = null;
  private timer: ReturnType<typeof setTimeout> | undefined;

  constructor(
    private readonly send: (p: Point) => boolean,
    private readonly now: () => number = () => performance.now(),
    private readonly intervalMs = 66,
  ) {}

  move(p: Point): void {
    this.pending = p;
    const wait = this.lastSent + this.intervalMs - this.now();
    if (wait <= 0) this.flush();
    else if (this.timer === undefined) this.timer = setTimeout(() => this.flush(), wait);
  }

  apply(updates: readonly CursorUpdate[]): void {
    if (updates.length === 0) return;
    for (const u of updates) {
      const id = Number(u.clientId);
      if (u.gone) this.others.delete(id);
      else this.others.set(id, { x: u.x, y: u.y });
    }
    this.changed();
  }

  /** Forget others' cursors (after reconnecting they are re-sent as people move). */
  reset(): void {
    this.others.clear();
    this.changed();
  }

  subscribe(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  private flush(): void {
    clearTimeout(this.timer);
    this.timer = undefined;
    if (!this.pending) return;
    if (this.send(this.pending)) {
      this.lastSent = this.now();
      this.pending = null;
    }
  }

  private changed(): void {
    for (const fn of this.listeners) fn();
  }
}
