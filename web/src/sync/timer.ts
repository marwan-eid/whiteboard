import { create } from "@bufbuild/protobuf";
import { OpSchema, ShapeType, type Op } from "../gen/whiteboard/v1/protocol_pb";
import { TIMER_ID, MAX_TIMER_MS } from "./boardWide";
import type { DocObject } from "./doc";

/**
 * The board's countdown timer (docs/ARCHITECTURE.md, "Synchronized timers").
 * It is one board-wide object: ends_at_ms is when it runs out, in server
 * time, or 0 when it is not running; remaining_ms is the full duration while
 * running and what is left while paused. Every change writes both fields in
 * one op, so concurrent changes resolve to one of them, whole.
 */
export type TimerState =
  | { kind: "idle" }
  | { kind: "running"; endsAtMs: number; durationMs: number }
  | { kind: "paused"; remainingMs: number };

export function timerState(o: DocObject | undefined): TimerState {
  const endsAtMs = Number(o?.props.endsAtMs ?? 0n);
  const remainingMs = Number(o?.props.remainingMs ?? 0n);
  if (endsAtMs > 0) return { kind: "running", endsAtMs, durationMs: remainingMs };
  if (remainingMs > 0) return { kind: "paused", remainingMs };
  return { kind: "idle" };
}

/** Milliseconds left at server time now (never negative). */
export function remainingAt(s: TimerState, now: number): number {
  switch (s.kind) {
    case "running":
      return Math.max(0, s.endsAtMs - now);
    case "paused":
      return s.remainingMs;
    case "idle":
      return 0;
  }
}

/** "m:ss", or "h:mm:ss" from an hour; rounds up so 0:00 means time is up. */
export function formatRemaining(ms: number): string {
  const total = Math.ceil(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = String(total % 60).padStart(2, "0");
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${s}` : `${m}:${s}`;
}

function timerOp(exists: boolean, endsAtMs: number, remainingMs: number): Op {
  return create(OpSchema, {
    id: TIMER_ID,
    props: {
      type: exists ? undefined : ShapeType.TIMER,
      endsAtMs: BigInt(Math.round(endsAtMs)),
      remainingMs: BigInt(Math.round(Math.min(MAX_TIMER_MS, Math.max(0, remainingMs)))),
    },
  });
}

/** The ops for each control, given the timer object (if any) and server time now. */
export const timerOps = {
  start(o: DocObject | undefined, durationMs: number, now: number): Op {
    const d = Math.min(MAX_TIMER_MS, durationMs);
    return timerOp(!!o, now + d, d);
  },
  pause(o: DocObject | undefined, now: number): Op | null {
    const s = timerState(o);
    return s.kind === "running" ? timerOp(true, 0, remainingAt(s, now)) : null;
  },
  resume(o: DocObject | undefined, now: number): Op | null {
    const s = timerState(o);
    return s.kind === "paused" ? timerOp(true, now + s.remainingMs, s.remainingMs) : null;
  },
  addMinute(o: DocObject | undefined, now: number): Op | null {
    const s = timerState(o);
    if (s.kind === "running") {
      const left = remainingAt(s, now) + 60_000;
      return timerOp(true, now + left, Math.max(s.durationMs, left));
    }
    return s.kind === "paused" ? timerOp(true, 0, s.remainingMs + 60_000) : null;
  },
  reset(o: DocObject | undefined): Op | null {
    return o ? timerOp(true, 0, 0) : null;
  },
};
