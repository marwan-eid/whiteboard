import type { History } from "../gen/whiteboard/v1/protocol_pb";
import { Doc } from "./doc";

export interface HistoryViewOptions {
  /** Asks the server for the board as of seq (reply goes to onHistory). */
  request: (seq: number) => boolean;
  restore: (seq: number) => boolean;
  /** The latest seq this client has seen. */
  head: () => number;
  debounceMs?: number;
}

/**
 * History mode: while active, the canvas shows the board as of a chosen
 * version, read-only, and live edits keep syncing underneath. Scrubbing asks
 * the server for states (debounced); replies for versions no longer wanted
 * are ignored.
 */
export class HistoryView {
  active = false;
  seq = 0;
  head = 0;
  wallMs = 0;
  loading = false;
  /** The board at seq, once it arrives. */
  doc: Doc | null = null;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private readonly listeners = new Set<() => void>();

  constructor(private readonly opts: HistoryViewOptions) {}

  open(): void {
    this.active = true;
    this.head = this.opts.head();
    this.seek(this.head);
  }

  seek(seq: number): void {
    if (!this.active) return;
    this.seq = Math.max(0, Math.min(seq, this.head));
    this.loading = true;
    clearTimeout(this.timer);
    this.timer = setTimeout(() => this.opts.request(this.seq), this.opts.debounceMs ?? 100);
    this.changed();
  }

  onHistory(h: History): void {
    if (!this.active || Number(h.seq) !== this.seq) return; // a stale reply
    this.doc = Doc.fromSnapshot(h.objects);
    this.wallMs = Number(h.wallMs);
    this.loading = false;
    this.changed();
  }

  /** Makes the live board look like the version shown, then leaves history mode. */
  restore(): void {
    if (this.active && this.seq < this.head) this.opts.restore(this.seq);
    this.close();
  }

  close(): void {
    clearTimeout(this.timer);
    this.active = false;
    this.doc = null;
    this.loading = false;
    this.changed();
  }

  subscribe(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  private changed(): void {
    for (const fn of this.listeners) fn();
  }
}
