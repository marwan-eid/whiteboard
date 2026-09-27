import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Camera } from "./camera";
import { ViewportTracker, needsResubscribe, subscriptionFor } from "./viewport";

describe("viewport subscription", () => {
  const view = { x: 0, y: 0, w: 1000, h: 500 };

  it("subscribes the visible area plus a margin", () => {
    expect(subscriptionFor(view, false)).toEqual({ x: -500, y: -250, w: 2000, h: 1000, lod: false });
  });

  it("resubscribes when the view leaves the subscription, LOD flips, or the view shrinks a lot", () => {
    const sub = subscriptionFor(view, false);
    expect(needsResubscribe(sub, { ...view, x: 400 }, false)).toBe(false); // still inside
    expect(needsResubscribe(sub, { ...view, x: 600 }, false)).toBe(true); // out the right side
    expect(needsResubscribe(sub, view, true)).toBe(true);
    expect(needsResubscribe(sub, { x: 0, y: 0, w: 200, h: 100 }, false)).toBe(true); // zoomed far in
    expect(needsResubscribe(undefined, view, false)).toBe(true);
  });
});

describe("ViewportTracker", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("follows the camera, sending at most every interval", () => {
    const camera = new Camera();
    const t = new ViewportTracker(camera, () => ({ w: 1000, h: 500 }), 80);
    const sent: { x: number; lod: boolean }[] = [];
    t.subscribe((v) => sent.push({ x: v.x, lod: v.lod }));

    camera.panBy(-100, 0); // still inside the margin
    vi.advanceTimersByTime(100);
    expect(sent).toEqual([]);

    camera.panBy(-400, 0);
    camera.panBy(-300, 0);
    vi.advanceTimersByTime(100);
    expect(sent).toEqual([{ x: 800 - 500, lod: false }]); // one update, for the latest position

    camera.zoomAt({ x: 0, y: 0 }, 0.1);
    vi.advanceTimersByTime(100);
    expect(sent.at(-1)?.lod).toBe(true);
  });
});
