import { describe, expect, it } from "vitest";
import { ClockSync } from "./clock";

describe("ClockSync", () => {
  it("is 0 with no information, then uses the welcome seed", () => {
    const c = new ClockSync();
    expect(c.offsetMs).toBe(0);
    c.seedFrom(10_500, 10_000);
    expect(c.offsetMs).toBe(500);
  });

  it("prefers the lowest-RTT sample", () => {
    const c = new ClockSync();
    c.seedFrom(0, 1_000_000);
    // Server is 300 ms ahead. A congested ping (rtt 400, reply delayed on the
    // way back) gives a biased estimate; the quick one is accurate.
    c.addSample(400, 1_300, 1_300); // biased: 1300 + 200 - 1300 = 200
    c.addSample(20, 1_310, 1_020); // 1310 + 10 - 1020 = 300
    expect(c.offsetMs).toBe(300);
  });

  it("forgets old samples", () => {
    const c = new ClockSync(2);
    c.addSample(1, 100, 0); // best, but will age out
    c.addSample(50, 1_000, 0);
    c.addSample(60, 2_000, 0);
    expect(c.offsetMs).toBe(1_025);
  });
});
