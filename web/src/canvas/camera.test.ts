import fc from "fast-check";
import { describe, expect, it } from "vitest";
import { Camera, MAX_ZOOM, MIN_ZOOM } from "./camera";

describe("Camera", () => {
  it("round-trips screen and world coordinates", () => {
    fc.assert(
      fc.property(
        fc.double({ min: -1e5, max: 1e5, noNaN: true }),
        fc.double({ min: -1e5, max: 1e5, noNaN: true }),
        fc.double({ min: MIN_ZOOM, max: MAX_ZOOM, noNaN: true }),
        fc.record({ x: fc.double({ min: 0, max: 3000, noNaN: true }), y: fc.double({ min: 0, max: 3000, noNaN: true }) }),
        (x, y, zoom, s) => {
          const c = new Camera();
          Object.assign(c, { x, y, zoom });
          const back = c.toScreen(c.toWorld(s));
          expect(back.x).toBeCloseTo(s.x, 6);
          expect(back.y).toBeCloseTo(s.y, 6);
        },
      ),
    );
  });

  it("zooms around the pointer", () => {
    const c = new Camera();
    c.panBy(-100, -50);
    const at = { x: 400, y: 300 };
    const before = c.toWorld(at);
    c.zoomAt(at, 2.5);
    const after = c.toWorld(at);
    expect(after.x).toBeCloseTo(before.x);
    expect(after.y).toBeCloseTo(before.y);
    expect(c.zoom).toBe(2.5);
  });

  it("clamps zoom and notifies listeners", () => {
    const c = new Camera();
    let calls = 0;
    c.subscribe(() => calls++);
    c.zoomAt({ x: 0, y: 0 }, 1e9);
    expect(c.zoom).toBe(MAX_ZOOM);
    c.zoomAt({ x: 0, y: 0 }, 1e-12);
    expect(c.zoom).toBe(MIN_ZOOM);
    expect(calls).toBe(2);
  });

  it("pans in screen pixels regardless of zoom", () => {
    const c = new Camera();
    c.zoomAt({ x: 0, y: 0 }, 2);
    c.panBy(100, 0);
    expect(c.x).toBe(-50);
    expect(c.visible(800, 600)).toEqual({ x: -50, y: 0, w: 400, h: 300 });
  });
});
