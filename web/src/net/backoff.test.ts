import { describe, expect, it } from "vitest";
import { backoffDelay } from "./backoff";

const opts = { baseMs: 100, capMs: 1_000 };

describe("backoffDelay", () => {
  it("stays within [ceiling/2, ceiling] and grows exponentially", () => {
    expect(backoffDelay(0, () => 0, opts)).toBe(50);
    expect(backoffDelay(0, () => 1, opts)).toBe(100);
    expect(backoffDelay(2, () => 0, opts)).toBe(200);
    expect(backoffDelay(2, () => 1, opts)).toBe(400);
  });

  it("is capped", () => {
    expect(backoffDelay(30, () => 1, opts)).toBe(1_000);
    expect(backoffDelay(30, () => 0, opts)).toBe(500);
  });

  it("treats negative attempts as the first attempt", () => {
    expect(backoffDelay(-3, () => 1, opts)).toBe(100);
  });
});
