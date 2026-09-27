import fc from "fast-check";
import { describe, expect, it } from "vitest";
import {
  boundsOf,
  clipToEllipseBorder,
  clipToRectBorder,
  decodePoints,
  distToSegment,
  ellipseContains,
  encodePoints,
  polylineHit,
  rectFromCorners,
  rectsIntersect,
  simplify,
} from "./geometry";

describe("points codec", () => {
  it("round-trips integer points", () => {
    fc.assert(
      fc.property(
        fc.array(fc.record({ x: fc.integer({ min: -1e7, max: 1e7 }), y: fc.integer({ min: -1e7, max: 1e7 }) })),
        (pts) => {
          expect(decodePoints(encodePoints(pts))).toEqual(pts);
        },
      ),
    );
  });

  it("is compact for smooth strokes", () => {
    const stroke = Array.from({ length: 100 }, (_, i) => ({ x: i * 3, y: Math.round(Math.sin(i / 5) * 20) }));
    expect(encodePoints(stroke).length).toBeLessThanOrEqual(200); // ~1 byte per coordinate
  });

  it("rounds to integers and rejects truncated input", () => {
    expect(decodePoints(encodePoints([{ x: 1.4, y: -2.6 }]))).toEqual([{ x: 1, y: -3 }]);
    expect(() => decodePoints(new Uint8Array([0x80]))).toThrow(/truncated/);
  });
});

describe("simplify", () => {
  it("keeps endpoints and drops near-collinear points", () => {
    const line = Array.from({ length: 50 }, (_, i) => ({ x: i, y: i % 2 === 0 ? 0 : 0.2 }));
    expect(simplify(line, 1)).toEqual([line[0], line[49]]);
  });

  it("keeps corners", () => {
    const corner = [
      { x: 0, y: 0 },
      { x: 5, y: 0 },
      { x: 10, y: 0 },
      { x: 10, y: 5 },
      { x: 10, y: 10 },
    ];
    expect(simplify(corner, 0.5)).toEqual([corner[0], corner[2], corner[4]]);
  });

  it("never moves the stroke by more than the tolerance", () => {
    fc.assert(
      fc.property(
        fc.array(fc.record({ x: fc.integer({ min: 0, max: 200 }), y: fc.integer({ min: 0, max: 200 }) }), { minLength: 2, maxLength: 60 }),
        (pts) => {
          const s = simplify(pts, 2);
          for (const p of pts) expect(polylineHit(p, s, 2 + 1e-9)).toBe(true);
        },
      ),
    );
  });
});

describe("shapes", () => {
  it("rectFromCorners normalizes", () => {
    expect(rectFromCorners({ x: 10, y: 5 }, { x: 2, y: 9 })).toEqual({ x: 2, y: 5, w: 8, h: 4 });
  });

  it("ellipseContains", () => {
    const r = { x: 0, y: 0, w: 100, h: 50 };
    expect(ellipseContains(r, { x: 50, y: 25 })).toBe(true);
    expect(ellipseContains(r, { x: 2, y: 2 })).toBe(false); // corner of the bbox, outside the ellipse
  });

  it("distToSegment and rectsIntersect", () => {
    expect(distToSegment({ x: 5, y: 3 }, { x: 0, y: 0 }, { x: 10, y: 0 })).toBe(3);
    expect(distToSegment({ x: -4, y: 3 }, { x: 0, y: 0 }, { x: 10, y: 0 })).toBe(5);
    expect(rectsIntersect({ x: 0, y: 0, w: 10, h: 10 }, { x: 10, y: 10, w: 5, h: 5 })).toBe(true);
    expect(rectsIntersect({ x: 0, y: 0, w: 10, h: 10 }, { x: 11, y: 0, w: 5, h: 5 })).toBe(false);
  });

  it("boundsOf", () => {
    expect(boundsOf([{ x: 3, y: -1 }, { x: -2, y: 4 }])).toEqual({ x: -2, y: -1, w: 5, h: 5 });
  });
});

describe("clipToEllipseBorder", () => {
  const circle = { x: 0, y: 0, w: 100, h: 100 };
  it("stops on the curve, not the bounding box", () => {
    const p = clipToEllipseBorder({ x: -100, y: -100 }, { x: 50, y: 50 }, circle);
    expect(Math.hypot(p.x - 50, p.y - 50)).toBeCloseTo(50, 6);
    expect(p.x).toBeCloseTo(50 - 50 / Math.SQRT2, 6);
  });
  it("leaves the target alone when the start is inside", () => {
    expect(clipToEllipseBorder({ x: 50, y: 40 }, { x: 50, y: 50 }, circle)).toEqual({ x: 50, y: 50 });
  });
});

describe("clipToRectBorder", () => {
  const box = { x: 100, y: 100, w: 100, h: 100 };
  it("stops at the border facing the start", () => {
    expect(clipToRectBorder({ x: 0, y: 150 }, { x: 150, y: 150 }, box)).toEqual({ x: 100, y: 150 });
    expect(clipToRectBorder({ x: 150, y: 400 }, { x: 150, y: 150 }, box)).toEqual({ x: 150, y: 200 });
  });
  it("leaves the target alone when the start is inside", () => {
    expect(clipToRectBorder({ x: 120, y: 120 }, { x: 150, y: 150 }, box)).toEqual({ x: 150, y: 150 });
  });
});
