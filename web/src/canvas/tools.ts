import { create } from "@bufbuild/protobuf";
import { generateKeyBetween } from "fractional-indexing";
import { OpSchema, ShapeType, type Binding, type ObjectProps, type Op } from "../gen/whiteboard/v1/protocol_pb";
import type { Gesture, History } from "../sync/history";
import type { Presence } from "../sync/presence";
import type { SyncSession } from "../sync/session";
import type { Camera } from "./camera";
import type { Editor, ToolId } from "./editor";
import { TOOLS } from "./editor";
import { boundsOf, encodePoints, rectFromCorners, simplify, type Point, type Rect } from "./geometry";
import type { Handle, Overlay, ResizeDir } from "./overlay";
import { measureTextHeight } from "./render";
import type { Scene } from "./scene";
import {
  DEFAULT_STROKE,
  STICKY_FILL,
  UNBOUND,
  anchorPoint,
  binding,
  bindingTarget,
  canHoldText,
  propRect,
  storedLine,
} from "./shapes";
import type { TextEditor } from "./textEditor";

export interface ControllerDeps {
  canvas: HTMLCanvasElement;
  session: SyncSession;
  history: History;
  scene: Scene;
  overlay: Overlay;
  editor: Editor;
  camera: Camera;
  presence: Presence;
  textEditor: TextEditor;
}

const CLICK_SLOP_PX = 3;
const MIN_SIZE = 4;
const DEFAULT_SIZE = {
  [ShapeType.RECT]: { w: 160, h: 100 },
  [ShapeType.ELLIPSE]: { w: 140, h: 100 },
  [ShapeType.STICKY]: { w: 200, h: 200 },
  [ShapeType.TEXT]: { w: 240, h: 0 },
} as const;

interface MoveOrigin {
  x: number;
  y: number;
  /** Arrows: where both ends were at the start, and what they were attached to. */
  ends?: [Point, Point];
  from?: Binding;
  to?: Binding;
}

type Drag =
  | { kind: "pan"; last: Point }
  | { kind: "marquee"; start: Point; base: Set<string> }
  | { kind: "move"; start: Point; startScreen: Point; origin: Map<string, MoveOrigin>; gesture: Gesture; moved: boolean; hit: string }
  | { kind: "resize"; id: string; dir: ResizeDir; rect: Rect; line: Point[]; start: Point; gesture: Gesture }
  | { kind: "end"; id: string; end: "from" | "to"; fixed: Point; gesture: Gesture }
  | { kind: "create"; type: ShapeType.RECT | ShapeType.ELLIPSE; start: Point; startScreen: Point; id: string | null; gesture: Gesture | null }
  | { kind: "arrow"; start: Point; startScreen: Point; from: Binding | null; id: string | null; gesture: Gesture | null }
  | { kind: "pen"; points: Point[] };

const op = (id: string, props: Partial<ObjectProps>): Op => create(OpSchema, { id, props });

/** Turns pointer, wheel and keyboard input into camera moves and board edits. */
export class Controller {
  private drag: Drag | null = null;
  // Text editing starts on pointerup: focusing the textarea during pointerdown
  // would be undone by the browser's own mousedown focus handling.
  private editOnUp: string | null = null;
  private spaceHeld = false;
  private pending: Point | null = null;
  private frameRequested = false;

  constructor(private readonly d: ControllerDeps) {
    const c = d.canvas;
    c.addEventListener("pointerdown", (e) => this.onPointerDown(e));
    c.addEventListener("pointermove", (e) => this.onPointerMove(e));
    c.addEventListener("pointerup", () => this.onPointerUp());
    c.addEventListener("pointercancel", () => this.cancel());
    c.addEventListener("dblclick", (e) => this.onDoubleClick(e));
    c.addEventListener("wheel", (e) => this.onWheel(e), { passive: false });
    c.addEventListener("contextmenu", (e) => e.preventDefault());
    window.addEventListener("keydown", (e) => this.onKeyDown(e));
    window.addEventListener("keyup", (e) => {
      if (e.key === " ") {
        this.spaceHeld = false;
        this.updateCursor();
      }
    });
    d.editor.subscribe(() => this.updateCursor());
    this.updateCursor();
  }

  // --- pointer --------------------------------------------------------------

  private screenPoint(e: MouseEvent): Point {
    const r = this.d.canvas.getBoundingClientRect();
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  }

  private onPointerDown(e: PointerEvent): void {
    if (this.d.textEditor.active) return; // the textarea's blur commits it
    this.d.canvas.setPointerCapture(e.pointerId);
    const s = this.screenPoint(e);
    const w = this.d.camera.toWorld(s);
    const tool = this.d.editor.tool;
    if (e.button === 1 || this.spaceHeld || tool === "hand") {
      this.drag = { kind: "pan", last: s };
      this.d.canvas.style.cursor = "grabbing";
      return;
    }
    if (e.button !== 0) return;
    switch (tool) {
      case "select":
        this.selectDown(s, w, e.shiftKey);
        break;
      case "rect":
      case "ellipse":
        this.drag = { kind: "create", type: tool === "rect" ? ShapeType.RECT : ShapeType.ELLIPSE, start: w, startScreen: s, id: null, gesture: null };
        break;
      case "sticky":
      case "text":
        this.createTextObject(tool === "sticky" ? ShapeType.STICKY : ShapeType.TEXT, w);
        break;
      case "arrow":
        this.drag = { kind: "arrow", start: w, startScreen: s, from: this.bindingAt(w, null), id: null, gesture: null };
        break;
      case "pen":
        this.drag = { kind: "pen", points: [w] };
        this.d.overlay.penPreview = this.drag.points;
        break;
    }
  }

  private selectDown(s: Point, w: Point, shift: boolean): void {
    const { editor, scene, overlay } = this.d;
    const h = overlay.handleAt(s);
    if (h) {
      this.startHandleDrag(h, w);
      return;
    }
    const hit = scene.hitTest(w);
    if (!hit) {
      if (!shift) editor.clearSelection();
      this.drag = { kind: "marquee", start: w, base: shift ? new Set(editor.selection) : new Set() };
      return;
    }
    if (shift) {
      editor.toggle(hit);
      if (!editor.selection.has(hit)) return;
    } else if (!editor.selection.has(hit)) {
      editor.select([hit]);
    }
    this.drag = { kind: "move", start: w, startScreen: s, origin: this.moveOrigin(), gesture: this.d.history.begin(), moved: false, hit };
  }

  private moveOrigin(): Map<string, MoveOrigin> {
    const out = new Map<string, MoveOrigin>();
    for (const id of this.d.editor.selection) {
      const o = this.d.session.doc.get(id);
      const g = this.d.scene.geometryOf(id);
      if (!o || !g) continue;
      if (o.props.type === ShapeType.ARROW && g.line && g.line.length >= 2) {
        out.set(id, { x: 0, y: 0, ends: [g.line[0]!, g.line[g.line.length - 1]!], from: o.props.from, to: o.props.to });
      } else {
        out.set(id, { x: o.props.x ?? 0, y: o.props.y ?? 0 });
      }
    }
    return out;
  }

  private startHandleDrag(h: Handle, w: Point): void {
    const o = this.d.session.doc.get(h.id);
    if (!o) return;
    const gesture = this.d.history.begin();
    if (h.kind === "resize") {
      const rect = this.d.scene.geometryOf(h.id)?.bounds ?? propRect(o.props);
      this.drag = { kind: "resize", id: h.id, dir: h.dir, rect, line: storedLine(o.props), start: w, gesture };
      return;
    }
    // Dragging one end of an arrow: the other end stays where it is (or attached).
    const other = h.end === "from" ? "to" : "from";
    const b = o.props[other];
    const target = bindingTarget(this.d.session.doc, b);
    const stored = storedLine(o.props);
    const fixed = target ? anchorPoint(target.props, b!) : (other === "from" ? stored[0] : stored[stored.length - 1]) ?? { x: 0, y: 0 };
    this.drag = { kind: "end", id: h.id, end: h.end, fixed, gesture };
  }

  private onPointerMove(e: PointerEvent): void {
    const s = this.screenPoint(e);
    this.d.presence.move(this.d.camera.toWorld(s));
    if (!this.drag) return;
    this.pending = s;
    if (this.frameRequested) return;
    this.frameRequested = true;
    requestAnimationFrame(() => {
      this.frameRequested = false;
      this.flushPointer();
    });
  }

  private flushPointer(): void {
    const s = this.pending;
    this.pending = null;
    if (s && this.drag) this.applyDrag(this.drag, s);
  }

  private applyDrag(drag: Drag, s: Point): void {
    const { camera, editor, scene, overlay, history, session } = this.d;
    const w = camera.toWorld(s);
    switch (drag.kind) {
      case "pan":
        camera.panBy(s.x - drag.last.x, s.y - drag.last.y);
        drag.last = s;
        break;
      case "marquee": {
        overlay.marquee = rectFromCorners(drag.start, w);
        editor.select([...drag.base, ...scene.inside(overlay.marquee)]);
        break;
      }
      case "move": {
        if (!drag.moved && Math.hypot(s.x - drag.startScreen.x, s.y - drag.startScreen.y) < CLICK_SLOP_PX) return;
        drag.moved = true;
        drag.gesture.edit(this.moveOps(drag.origin, w.x - drag.start.x, w.y - drag.start.y));
        break;
      }
      case "resize":
        drag.gesture.edit([this.resizeOp(drag, w)]);
        break;
      case "end": {
        const target = this.bindingAt(w, drag.id);
        const [a, b] = drag.end === "from" ? [w, drag.fixed] : [drag.fixed, w];
        drag.gesture.edit([op(drag.id, { ...arrowGeometry(a, b), [drag.end]: target ?? UNBOUND })]);
        break;
      }
      case "create": {
        if (!drag.id) {
          if (Math.hypot(s.x - drag.startScreen.x, s.y - drag.startScreen.y) < CLICK_SLOP_PX) return;
          drag.id = session.newObjectId();
          drag.gesture = history.begin();
          drag.gesture.edit([op(drag.id, { type: drag.type, ...this.newStyle(drag.type), ...rectFromCorners(drag.start, w) })]);
        } else {
          drag.gesture!.edit([op(drag.id, rectFromCorners(drag.start, w))]);
        }
        break;
      }
      case "arrow": {
        if (!drag.id) {
          if (Math.hypot(s.x - drag.startScreen.x, s.y - drag.startScreen.y) < CLICK_SLOP_PX) return;
          drag.id = session.newObjectId();
          drag.gesture = history.begin();
          drag.gesture.edit([
            op(drag.id, {
              type: ShapeType.ARROW,
              z: this.nextZ(),
              stroke: DEFAULT_STROKE,
              strokeWidth: 2,
              from: drag.from ?? UNBOUND,
              to: UNBOUND,
              ...arrowGeometry(drag.start, w),
            }),
          ]);
        } else {
          drag.gesture!.edit([op(drag.id, { ...arrowGeometry(drag.start, w), to: this.bindingAt(w, drag.id) ?? UNBOUND })]);
        }
        break;
      }
      case "pen": {
        const last = drag.points[drag.points.length - 1]!;
        if (Math.hypot(w.x - last.x, w.y - last.y) * camera.zoom >= 1) drag.points.push(w);
        break;
      }
    }
    overlay.invalidate();
  }

  private onPointerUp(): void {
    this.flushPointer();
    if (this.editOnUp) {
      this.d.textEditor.open(this.editOnUp);
      this.editOnUp = null;
    }
    const drag = this.drag;
    this.drag = null;
    if (!drag) return;
    const { editor, overlay, history, session } = this.d;
    switch (drag.kind) {
      case "pan":
        this.updateCursor();
        break;
      case "marquee":
        overlay.marquee = null;
        break;
      case "move":
        if (drag.moved) drag.gesture.end();
        else if (editor.selection.size > 1 && editor.selection.has(drag.hit)) editor.select([drag.hit]);
        break;
      case "resize":
      case "end":
        drag.gesture.end();
        break;
      case "create": {
        let id = drag.id;
        if (!id) {
          // A click without dragging makes a default-size shape centered there.
          id = session.newObjectId();
          const size = DEFAULT_SIZE[drag.type];
          history.apply([
            op(id, { type: drag.type, ...this.newStyle(drag.type), x: Math.round(drag.start.x - size.w / 2), y: Math.round(drag.start.y - size.h / 2), ...size }),
          ]);
        } else {
          drag.gesture!.end();
        }
        editor.setTool("select");
        editor.select([id]);
        break;
      }
      case "arrow":
        if (drag.id) {
          drag.gesture!.end();
          editor.setTool("select");
          editor.select([drag.id]);
        }
        break;
      case "pen":
        this.finishStroke(drag.points);
        overlay.penPreview = null;
        break;
    }
    overlay.invalidate();
  }

  /** Escape or a cancelled pointer: undo the gesture in progress. */
  private cancel(): void {
    const drag = this.drag;
    this.drag = null;
    if (!drag) return;
    if ("gesture" in drag && drag.gesture) drag.gesture.cancel();
    this.d.overlay.marquee = null;
    this.d.overlay.penPreview = null;
    this.d.overlay.invalidate();
    this.updateCursor();
  }

  private onDoubleClick(e: MouseEvent): void {
    if (this.d.editor.tool !== "select") return;
    const w = this.d.camera.toWorld(this.screenPoint(e));
    const hit = this.d.scene.hitTest(w);
    if (hit) {
      if (canHoldText(this.d.session.doc.get(hit)?.props.type)) this.d.textEditor.open(hit);
      return;
    }
    // Double-clicking empty space adds a rectangle there.
    const id = this.d.session.newObjectId();
    const size = DEFAULT_SIZE[ShapeType.RECT];
    this.d.history.apply([
      op(id, { type: ShapeType.RECT, ...this.newStyle(ShapeType.RECT), x: Math.round(w.x - size.w / 2), y: Math.round(w.y - size.h / 2), ...size }),
    ]);
    this.d.editor.select([id]);
  }

  private onWheel(e: WheelEvent): void {
    e.preventDefault();
    const unit = e.deltaMode === WheelEvent.DOM_DELTA_LINE ? 16 : 1;
    if (e.ctrlKey || e.metaKey) {
      // Pinch-zoom on trackpads arrives as ctrl+wheel.
      this.d.camera.zoomAt(this.screenPoint(e), Math.exp(-e.deltaY * unit * 0.01));
    } else {
      this.d.camera.panBy(-e.deltaX * unit, -e.deltaY * unit);
    }
  }

  // --- keyboard -------------------------------------------------------------

  private onKeyDown(e: KeyboardEvent): void {
    const target = e.target as HTMLElement | null;
    if (this.d.textEditor.active || target?.tagName === "TEXTAREA" || target?.tagName === "INPUT") return;
    const { editor, history, camera, scene } = this.d;
    const mod = e.ctrlKey || e.metaKey;
    const key = e.key.toLowerCase();

    if (e.key === " ") {
      this.spaceHeld = true;
      this.updateCursor();
      e.preventDefault();
      return;
    }
    if (mod && key === "z") {
      e.preventDefault();
      if (e.shiftKey) history.redo();
      else history.undo();
      return;
    }
    if (mod && key === "y") {
      e.preventDefault();
      history.redo();
      return;
    }
    if (mod && key === "a") {
      e.preventDefault();
      editor.setTool("select");
      editor.select(scene.all());
      return;
    }
    if (mod && (key === "=" || key === "+" || key === "-" || key === "0")) {
      e.preventDefault();
      const center = { x: this.d.canvas.clientWidth / 2, y: this.d.canvas.clientHeight / 2 };
      if (key === "0") camera.zoomAt(center, 1 / camera.zoom);
      else camera.zoomAt(center, key === "-" ? 1 / 1.25 : 1.25);
      return;
    }
    if (e.key === "Delete" || e.key === "Backspace") {
      if (editor.selection.size === 0) return;
      e.preventDefault();
      history.apply([...editor.selection].map((id) => op(id, { deleted: true })));
      editor.clearSelection();
      return;
    }
    if (e.key === "Escape") {
      if (this.drag) this.cancel();
      else {
        editor.clearSelection();
        editor.setTool("select");
      }
      return;
    }
    if (e.key === "Enter" && editor.selection.size === 1) {
      const [id] = editor.selection;
      if (canHoldText(this.d.session.doc.get(id!)?.props.type)) {
        e.preventDefault();
        this.d.textEditor.open(id!);
      }
      return;
    }
    if (!mod && !e.altKey) {
      const tool = TOOLS.find((t) => t.key === key);
      if (tool) editor.setTool(tool.id);
    }
  }

  // --- edits ----------------------------------------------------------------

  private moveOps(origin: Map<string, MoveOrigin>, dx: number, dy: number): Op[] {
    const ops: Op[] = [];
    for (const [id, o] of origin) {
      if (!o.ends) {
        ops.push(op(id, { x: Math.round(o.x + dx), y: Math.round(o.y + dy) }));
        continue;
      }
      // An arrow keeps an attachment only if its target moves with it.
      const [a, b] = o.ends.map((p) => ({ x: p.x + dx, y: p.y + dy })) as [Point, Point];
      const keep = (bd?: Binding) => (bd?.objectId && origin.has(bd.objectId) ? bd : UNBOUND);
      ops.push(op(id, { ...arrowGeometry(a, b), from: keep(o.from), to: keep(o.to) }));
    }
    return ops;
  }

  private resizeOp(drag: Extract<Drag, { kind: "resize" }>, w: Point): Op {
    // Move the grabbed edges by how far the pointer moved, so grabbing a
    // handle slightly off-center does not make the shape jump.
    const r = drag.rect;
    const dx = w.x - drag.start.x;
    const dy = w.y - drag.start.y;
    let x1 = r.x;
    let y1 = r.y;
    let x2 = r.x + r.w;
    let y2 = r.y + r.h;
    if (drag.dir.includes("w")) x1 += dx;
    if (drag.dir.includes("e")) x2 += dx;
    if (drag.dir.includes("n")) y1 += dy;
    if (drag.dir.includes("s")) y2 += dy;
    const n = rectFromCorners({ x: x1, y: y1 }, { x: x2, y: y2 });
    n.w = Math.max(MIN_SIZE, Math.round(n.w));
    n.h = Math.max(MIN_SIZE, Math.round(n.h));
    n.x = Math.round(n.x);
    n.y = Math.round(n.y);
    const o = this.d.session.doc.get(drag.id)!;
    switch (o.props.type) {
      case ShapeType.FREEHAND: {
        const sx = r.w > 0 ? n.w / r.w : 1;
        const sy = r.h > 0 ? n.h / r.h : 1;
        const rel = drag.line.map((p) => ({ x: (p.x - r.x) * sx, y: (p.y - r.y) * sy }));
        return op(drag.id, { x: n.x, y: n.y, w: n.w, h: n.h, points: encodePoints(rel) });
      }
      case ShapeType.TEXT:
        // Text height follows its content; only the wrap width is resizable.
        return op(drag.id, { x: n.x, y: n.y, w: n.w, h: measureTextHeight({ ...o.props, w: n.w }, o.props.text ?? "") });
      default:
        return op(drag.id, n);
    }
  }

  private createTextObject(type: ShapeType.STICKY | ShapeType.TEXT, w: Point): void {
    const { session, history, editor } = this.d;
    const id = session.newObjectId();
    const size = DEFAULT_SIZE[type];
    const props: Partial<ObjectProps> = { type, ...this.newStyle(type), text: "", w: size.w };
    props.h = type === ShapeType.TEXT ? measureTextHeight(props as ObjectProps, "") : size.h;
    props.x = Math.round(w.x - size.w / 2);
    props.y = Math.round(w.y - props.h / 2);
    history.apply([op(id, props)]);
    editor.setTool("select");
    editor.select([id]);
    this.editOnUp = id;
  }

  private finishStroke(points: Point[]): void {
    const pts = simplify(points, 1 / this.d.camera.zoom).map((p) => ({ x: Math.round(p.x), y: Math.round(p.y) }));
    const b = boundsOf(pts);
    const rel = pts.map((p) => ({ x: p.x - b.x, y: p.y - b.y }));
    this.d.history.apply([
      op(this.d.session.newObjectId(), {
        type: ShapeType.FREEHAND,
        z: this.nextZ(),
        stroke: DEFAULT_STROKE,
        strokeWidth: 3,
        ...b,
        points: encodePoints(rel),
      }),
    ]);
  }

  /** An attachment for an arrow end at w: the topmost non-arrow object there. */
  private bindingAt(w: Point, arrowId: string | null): Binding | null {
    const doc = this.d.session.doc;
    const id = this.d.scene.hitTest(w, 6, (cand) => cand !== arrowId && doc.get(cand)?.props.type !== ShapeType.ARROW);
    const target = id ? doc.get(id) : undefined;
    return id && target ? binding(id, target.props, w) : null;
  }

  private newStyle(type: ShapeType): Partial<ObjectProps> {
    const z = this.nextZ();
    switch (type) {
      case ShapeType.STICKY:
        return { z, fill: STICKY_FILL };
      case ShapeType.TEXT:
        return { z, stroke: DEFAULT_STROKE };
      default:
        return { z, fill: 0xffffffff, stroke: DEFAULT_STROKE, strokeWidth: 1.5 };
    }
  }

  private nextZ(): string {
    return generateKeyBetween(this.d.scene.maxZ(), null);
  }

  private updateCursor(): void {
    const tool: ToolId = this.d.editor.tool;
    this.d.canvas.style.cursor = this.spaceHeld || tool === "hand" ? "grab" : tool === "select" ? "default" : "crosshair";
  }
}

/** Stored arrow geometry for two board points: origin, size and relative points. */
export function arrowGeometry(a: Point, b: Point): Partial<ObjectProps> {
  const r = boundsOf([a, b]);
  const x = Math.round(r.x);
  const y = Math.round(r.y);
  return { x, y, w: Math.round(r.w), h: Math.round(r.h), points: encodePoints([{ x: a.x - x, y: a.y - y }, { x: b.x - x, y: b.y - y }]) };
}
