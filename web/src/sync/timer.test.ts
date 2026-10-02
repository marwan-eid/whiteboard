import { describe, expect, it } from "vitest";
import { Doc } from "./doc";
import { TIMER_ID } from "./boardWide";
import { formatRemaining, remainingAt, timerOps, timerState } from "./timer";
import type { Op } from "../gen/whiteboard/v1/protocol_pb";

describe("timer", () => {
  it("runs, pauses, resumes, gains a minute and resets", () => {
    const doc = new Doc();
    let wall = 0;
    const apply = (op: Op | null) => {
      expect(op).not.toBeNull();
      doc.apply(op!, { wallMs: ++wall, counter: 0, clientId: 1 });
    };
    const timer = () => doc.get(TIMER_ID);

    expect(timerState(timer())).toEqual({ kind: "idle" });
    apply(timerOps.start(timer(), 5 * 60_000, 1_000));
    expect(timer()?.props.type).toBeDefined();
    expect(timerState(timer())).toEqual({ kind: "running", endsAtMs: 301_000, durationMs: 300_000 });
    expect(remainingAt(timerState(timer()), 61_000)).toBe(240_000);

    apply(timerOps.pause(timer(), 61_000));
    expect(timerState(timer())).toEqual({ kind: "paused", remainingMs: 240_000 });
    expect(timerOps.pause(timer(), 70_000)).toBeNull();

    apply(timerOps.resume(timer(), 100_000));
    expect(remainingAt(timerState(timer()), 100_000)).toBe(240_000);
    apply(timerOps.addMinute(timer(), 100_000));
    expect(remainingAt(timerState(timer()), 100_000)).toBe(300_000);
    expect(remainingAt(timerState(timer()), 10_000_000)).toBe(0);

    apply(timerOps.reset(timer()));
    expect(timerState(timer())).toEqual({ kind: "idle" });
  });

  it("formats rounding up", () => {
    expect(formatRemaining(0)).toBe("0:00");
    expect(formatRemaining(1)).toBe("0:01");
    expect(formatRemaining(59_001)).toBe("1:00");
    expect(formatRemaining(3_600_000)).toBe("1:00:00");
    expect(formatRemaining(3_725_000)).toBe("1:02:05");
  });
});
