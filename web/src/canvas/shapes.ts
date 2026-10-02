import { create } from "@bufbuild/protobuf";
import { BindingSchema, ShapeType, type Binding, type ObjectProps } from "../gen/whiteboard/v1/protocol_pb";
import { isShape, type Doc, type DocObject } from "../sync/doc";
import {
  boundsOf,
  clipToEllipseBorder,
  clipToRectBorder,
  decodePoints,
  ellipseContains,
  polylineHit,
  rectContains,
  type Point,
  type Rect,
} from "./geometry";

export const DEFAULT_STROKE = 0x1f2328ff;
export const STICKY_FILL = 0xfff3b0ff;
export const STICKY_FONT_SIZE = 18;
export const TEXT_FONT_SIZE = 24;
export const TEXT_PADDING = 8;

/** Where an object is in board coordinates, with derived geometry resolved. */
export interface Geometry {
  type: ShapeType;
  bounds: Rect;
  /** Arrow (2 points) or freehand polyline, in board coordinates. */
  line?: Point[];
}

export function propRect(p: ObjectProps): Rect {
  return { x: p.x ?? 0, y: p.y ?? 0, w: p.w ?? 0, h: p.h ?? 0 };
}

/** The stored points of an arrow or stroke, in board coordinates. */
export function storedLine(p: ObjectProps): Point[] {
  const ox = p.x ?? 0;
  const oy = p.y ?? 0;
  if (!p.points || p.points.length === 0) return [];
  try {
    return decodePoints(p.points).map((q) => ({ x: q.x + ox, y: q.y + oy }));
  } catch {
    return [];
  }
}

/** A binding's target, if it is attached to a visible object. */
export function bindingTarget(doc: Doc, b: Binding | undefined): DocObject | undefined {
  if (!b?.objectId) return undefined;
  const t = doc.get(b.objectId);
  return t && isShape(t) && t.props.type !== ShapeType.ARROW ? t : undefined;
}

export function anchorPoint(target: ObjectProps, b: Binding, r: Rect = propRect(target)): Point {
  return { x: r.x + b.anchorX * r.w, y: r.y + b.anchorY * r.h };
}

/**
 * Arrow endpoints: an attached end follows its target (stopping at the
 * target's border); a detached end uses the stored point. `moved` gives
 * targets' boxes that differ from the document (during a drag).
 */
export function arrowEnds(doc: Doc, p: ObjectProps, moved?: ReadonlyMap<string, Rect>): [Point, Point] | null {
  const stored = storedLine(p);
  if (stored.length < 2) return null;
  const fromT = bindingTarget(doc, p.from);
  const toT = bindingTarget(doc, p.to);
  const rectOf = (t: DocObject) => moved?.get(t.id) ?? propRect(t.props);
  const rawStart = fromT ? anchorPoint(fromT.props, p.from!, rectOf(fromT)) : stored[0]!;
  const rawEnd = toT ? anchorPoint(toT.props, p.to!, rectOf(toT)) : stored[stored.length - 1]!;
  const start = fromT ? clipToBorder(rawEnd, rawStart, fromT.props.type, rectOf(fromT)) : rawStart;
  const end = toT ? clipToBorder(rawStart, rawEnd, toT.props.type, rectOf(toT)) : rawEnd;
  return [start, end];
}

/** Where the segment from `from` towards `to` meets the target's outline. */
function clipToBorder(from: Point, to: Point, type: ShapeType | undefined, r: Rect): Point {
  return type === ShapeType.ELLIPSE ? clipToEllipseBorder(from, to, r) : clipToRectBorder(from, to, r);
}

export function geometry(doc: Doc, o: DocObject): Geometry {
  const p = o.props;
  const type = p.type ?? ShapeType.RECT;
  switch (type) {
    case ShapeType.ARROW: {
      const ends = arrowEnds(doc, p);
      return ends ? { type, bounds: boundsOf(ends), line: ends } : { type, bounds: propRect(p) };
    }
    case ShapeType.FREEHAND: {
      const line = storedLine(p);
      return { type, bounds: line.length ? boundsOf(line) : propRect(p), line };
    }
    default:
      return { type, bounds: propRect(p) };
  }
}

/** tolerance is in board units (callers scale a screen tolerance by zoom). */
export function hitTest(g: Geometry, p: Point, tolerance: number): boolean {
  switch (g.type) {
    case ShapeType.ELLIPSE:
      return ellipseContains(g.bounds, p, tolerance);
    case ShapeType.ARROW:
    case ShapeType.FREEHAND:
      return g.line ? polylineHit(p, g.line, tolerance) : false;
    default:
      return rectContains(g.bounds, p, tolerance);
  }
}

/** Ids of the objects an arrow is attached to. */
export function bindingIds(p: ObjectProps): string[] {
  return [p.from?.objectId, p.to?.objectId].filter((x): x is string => !!x);
}

export function binding(objectId: string, target: ObjectProps, at: Point): Binding {
  const r = propRect(target);
  const clamp = (v: number) => Math.min(1, Math.max(0, v));
  return create(BindingSchema, {
    objectId,
    anchorX: r.w > 0 ? clamp((at.x - r.x) / r.w) : 0.5,
    anchorY: r.h > 0 ? clamp((at.y - r.y) / r.h) : 0.5,
  });
}

export const UNBOUND: Binding = create(BindingSchema, {});

export function canHoldText(type: ShapeType | undefined): boolean {
  return type === ShapeType.STICKY || type === ShapeType.TEXT;
}
