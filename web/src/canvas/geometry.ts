export interface Point {
  x: number;
  y: number;
}

export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}

export function rectFromCorners(a: Point, b: Point): Rect {
  return { x: Math.min(a.x, b.x), y: Math.min(a.y, b.y), w: Math.abs(a.x - b.x), h: Math.abs(a.y - b.y) };
}

export function rectContains(r: Rect, p: Point, pad = 0): boolean {
  return p.x >= r.x - pad && p.x <= r.x + r.w + pad && p.y >= r.y - pad && p.y <= r.y + r.h + pad;
}

export function rectsIntersect(a: Rect, b: Rect): boolean {
  return a.x <= b.x + b.w && b.x <= a.x + a.w && a.y <= b.y + b.h && b.y <= a.y + a.h;
}

/** a fully inside b */
export function rectInside(a: Rect, b: Rect): boolean {
  return a.x >= b.x && a.y >= b.y && a.x + a.w <= b.x + b.w && a.y + a.h <= b.y + b.h;
}

export function boundsOf(points: readonly Point[]): Rect {
  if (points.length === 0) return { x: 0, y: 0, w: 0, h: 0 };
  let minX = Infinity;
  let minY = Infinity;
  let maxX = -Infinity;
  let maxY = -Infinity;
  for (const p of points) {
    minX = Math.min(minX, p.x);
    minY = Math.min(minY, p.y);
    maxX = Math.max(maxX, p.x);
    maxY = Math.max(maxY, p.y);
  }
  return { x: minX, y: minY, w: maxX - minX, h: maxY - minY };
}

export function ellipseContains(r: Rect, p: Point, pad = 0): boolean {
  const rx = r.w / 2 + pad;
  const ry = r.h / 2 + pad;
  if (rx <= 0 || ry <= 0) return false;
  const dx = p.x - (r.x + r.w / 2);
  const dy = p.y - (r.y + r.h / 2);
  return (dx * dx) / (rx * rx) + (dy * dy) / (ry * ry) <= 1;
}

export function distToSegment(p: Point, a: Point, b: Point): number {
  const dx = b.x - a.x;
  const dy = b.y - a.y;
  const len2 = dx * dx + dy * dy;
  const t = len2 === 0 ? 0 : Math.max(0, Math.min(1, ((p.x - a.x) * dx + (p.y - a.y) * dy) / len2));
  return Math.hypot(p.x - (a.x + t * dx), p.y - (a.y + t * dy));
}

export function polylineHit(p: Point, points: readonly Point[], tolerance: number): boolean {
  if (points.length === 1) return Math.hypot(p.x - points[0]!.x, p.y - points[0]!.y) <= tolerance;
  for (let i = 1; i < points.length; i++) {
    if (distToSegment(p, points[i - 1]!, points[i]!) <= tolerance) return true;
  }
  return false;
}

/**
 * Ramer-Douglas-Peucker: drops points that deviate less than tolerance from
 * the simplified line, so freehand strokes stay small on the wire.
 */
export function simplify(points: readonly Point[], tolerance: number): Point[] {
  if (points.length <= 2) return [...points];
  const keep = new Uint8Array(points.length);
  keep[0] = keep[points.length - 1] = 1;
  const stack: [number, number][] = [[0, points.length - 1]];
  while (stack.length) {
    const [lo, hi] = stack.pop()!;
    let worst = -1;
    let worstDist = tolerance;
    for (let i = lo + 1; i < hi; i++) {
      const d = distToSegment(points[i]!, points[lo]!, points[hi]!);
      if (d > worstDist) {
        worst = i;
        worstDist = d;
      }
    }
    if (worst >= 0) {
      keep[worst] = 1;
      stack.push([lo, worst], [worst, hi]);
    }
  }
  return points.filter((_, i) => keep[i]);
}

/**
 * Where the segment from `from` towards `to` first touches rect r, if `from`
 * is outside it (Liang-Barsky). Used to end arrows at a shape's border.
 */
export function clipToRectBorder(from: Point, to: Point, r: Rect): Point {
  if (rectContains(r, from)) return to;
  const dx = to.x - from.x;
  const dy = to.y - from.y;
  let t0 = 0;
  let t1 = 1;
  const edges: [number, number][] = [
    [-dx, from.x - r.x],
    [dx, r.x + r.w - from.x],
    [-dy, from.y - r.y],
    [dy, r.y + r.h - from.y],
  ];
  for (const [p, q] of edges) {
    if (p === 0) {
      if (q < 0) return to;
    } else {
      const t = q / p;
      if (p < 0) t0 = Math.max(t0, t);
      else t1 = Math.min(t1, t);
    }
  }
  if (t0 > t1) return to;
  return { x: from.x + t0 * dx, y: from.y + t0 * dy };
}

// --- points codec -----------------------------------------------------------
// Integer points as zigzag varints of the delta from the previous point
// (the first from the origin). Mirrors the note on ObjectProps.points.

export function encodePoints(points: readonly Point[]): Uint8Array<ArrayBuffer> {
  const out: number[] = [];
  let px = 0;
  let py = 0;
  for (const p of points) {
    const x = Math.round(p.x);
    const y = Math.round(p.y);
    writeVarint(out, zigzag(x - px));
    writeVarint(out, zigzag(y - py));
    px = x;
    py = y;
  }
  return new Uint8Array(out);
}

export function decodePoints(bytes: Uint8Array): Point[] {
  const out: Point[] = [];
  let i = 0;
  let x = 0;
  let y = 0;
  const read = (): number => {
    let v = 0;
    let shift = 1;
    for (;;) {
      if (i >= bytes.length) throw new Error("truncated points");
      const b = bytes[i++]!;
      v += (b & 0x7f) * shift;
      if (b < 0x80) return v;
      shift *= 128;
    }
  };
  while (i < bytes.length) {
    x += unzigzag(read());
    y += unzigzag(read());
    out.push({ x, y });
  }
  return out;
}

function zigzag(n: number): number {
  return n >= 0 ? n * 2 : -n * 2 - 1;
}

function unzigzag(n: number): number {
  return n % 2 === 0 ? n / 2 : -(n + 1) / 2;
}

function writeVarint(out: number[], v: number): void {
  while (v >= 0x80) {
    out.push((v % 128) | 0x80);
    v = Math.floor(v / 128);
  }
  out.push(v);
}

/** Like clipToRectBorder, for the ellipse inscribed in r. */
export function clipToEllipseBorder(from: Point, to: Point, r: Rect): Point {
  if (ellipseContains(r, from)) return to;
  const rx = r.w / 2;
  const ry = r.h / 2;
  if (rx <= 0 || ry <= 0) return to;
  const cx = r.x + rx;
  const cy = r.y + ry;
  // Points along the segment, in coordinates where the ellipse is the unit circle.
  const px = (from.x - cx) / rx;
  const py = (from.y - cy) / ry;
  const dx = (to.x - from.x) / rx;
  const dy = (to.y - from.y) / ry;
  const a = dx * dx + dy * dy;
  const b = 2 * (px * dx + py * dy);
  const c = px * px + py * py - 1;
  const disc = b * b - 4 * a * c;
  if (a === 0 || disc < 0) return to;
  const t = (-b - Math.sqrt(disc)) / (2 * a);
  if (t < 0 || t > 1) return to;
  return { x: from.x + t * (to.x - from.x), y: from.y + t * (to.y - from.y) };
}
