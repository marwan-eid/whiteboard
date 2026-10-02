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
  private player: ReturnType<typeof setInterval> | undefined;
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

  get playing(): boolean {
    return this.player !== undefined;
  }

  /**
   * Replays history: steps forward about 80 times from the version shown (or
   * from the start, if already at the latest), waiting for each state before
   * the next, and stops at the latest version.
   */
  play(stepMs = 150): void {
    if (!this.active || this.playing) return;
    if (this.seq >= this.head) this.seek(0);
    const step = Math.max(1, Math.ceil(this.head / 80));
    this.player = setInterval(() => {
      if (this.loading) return;
      if (this.seq >= this.head) {
        this.pause();
        return;
      }
      this.seek(this.seq + step);
    }, stepMs);
    this.changed();
  }

  pause(): void {
    clearInterval(this.player);
    this.player = undefined;
    this.changed();
  }

  /** Makes the live board look like the version shown, then leaves history mode. */
  restore(): void {
    if (this.active && this.seq < this.head) this.opts.restore(this.seq);
    this.close();
  }

  close(): void {
    clearTimeout(this.timer);
    clearInterval(this.player);
    this.player = undefined;
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
