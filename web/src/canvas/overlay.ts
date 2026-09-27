import { Container, Graphics, Text } from "pixi.js";
import { ShapeType } from "../gen/whiteboard/v1/protocol_pb";
import { cursorColor, cursorName, type Presence } from "../sync/presence";
import type { Camera } from "./camera";
import type { Editor } from "./editor";
import type { FrameScheduler } from "./frames";
import type { Point, Rect } from "./geometry";
import type { Scene } from "./scene";

const ACCENT = 0x0969da;
const HANDLE = 8;

export type ResizeDir = "nw" | "n" | "ne" | "e" | "se" | "s" | "sw" | "w";

export type Handle = { kind: "resize"; id: string; dir: ResizeDir } | { kind: "end"; id: string; end: "from" | "to" };

const DIRS: readonly ResizeDir[] = ["nw", "n", "ne", "e", "se", "s", "sw", "w"];

/** Where each resize handle sits on a rect, as fractions of width and height. */
export const HANDLE_POS: Record<ResizeDir, [number, number]> = {
  nw: [0, 0],
  n: [0.5, 0],
  ne: [1, 0],
  e: [1, 0.5],
  se: [1, 1],
  s: [0.5, 1],
  sw: [0, 1],
  w: [0, 0.5],
};

/**
 * Screen-space layer above the board: selection, handles, marquee, pen
 * preview and other people's cursors. Redraws at most once per frame.
 */
export class Overlay {
  readonly layer = new Container();
  private readonly g = new Graphics();
  private readonly cursorLayer = new Container();
  private readonly cursorViews = new Map<number, { g: Graphics; label: Container }>();
  private dirty = false;

  marquee: Rect | null = null;
  penPreview: Point[] | null = null;

  constructor(
    private readonly scene: Scene,
    private readonly editor: Editor,
    private readonly camera: Camera,
    private readonly presence: Presence,
    private readonly frames: FrameScheduler,
  ) {
    this.layer.addChild(this.g, this.cursorLayer);
    frames.beforeRender(() => {
      if (!this.dirty) return;
      this.dirty = false;
      this.draw();
    });
    camera.subscribe(() => this.invalidate());
    editor.subscribe(() => this.invalidate());
    presence.subscribe(() => this.invalidate());
  }

  invalidate(): void {
    this.dirty = true;
    this.frames.request();
  }

  /** The handle under a screen point, if any. */
  handleAt(s: Point): Handle | null {
    for (const [h, at] of this.handles()) {
      if (Math.abs(s.x - at.x) <= HANDLE && Math.abs(s.y - at.y) <= HANDLE) return h;
    }
    return null;
  }

  private handles(): [Handle, Point][] {
    if (this.editor.selection.size !== 1 || this.editor.editing) return [];
    const [id] = this.editor.selection;
    const geom = this.scene.geometryOf(id!);
    if (!geom) return [];
    if (geom.type === ShapeType.ARROW) {
      if (!geom.line || geom.line.length < 2) return [];
      return [
        [{ kind: "end", id: id!, end: "from" }, this.camera.toScreen(geom.line[0]!)],
        [{ kind: "end", id: id!, end: "to" }, this.camera.toScreen(geom.line[geom.line.length - 1]!)],
      ];
    }
    const r = this.screenRect(geom.bounds);
    return DIRS.map((dir) => {
      const [fx, fy] = HANDLE_POS[dir];
      return [{ kind: "resize", id: id!, dir }, { x: r.x + fx * r.w, y: r.y + fy * r.h }];
    });
  }

  private screenRect(b: Rect): Rect {
    const tl = this.camera.toScreen(b);
    return { x: tl.x, y: tl.y, w: b.w * this.camera.zoom, h: b.h * this.camera.zoom };
  }

  private draw(): void {
    const g = this.g;
    g.clear();
    for (const id of this.editor.selection) {
      const geom = this.scene.geometryOf(id);
      if (!geom) continue;
      if (geom.type === ShapeType.ARROW && geom.line) {
        const [a, b] = [this.camera.toScreen(geom.line[0]!), this.camera.toScreen(geom.line[geom.line.length - 1]!)];
        g.moveTo(a.x, a.y).lineTo(b.x, b.y).stroke({ width: 1, color: ACCENT, alpha: 0.6 });
      } else {
        const r = this.screenRect(geom.bounds);
        g.rect(r.x - 3, r.y - 3, r.w + 6, r.h + 6).stroke({ width: 1.5, color: ACCENT });
      }
    }
    for (const [h, at] of this.handles()) {
      if (h.kind === "end") g.circle(at.x, at.y, HANDLE / 2 + 1);
      else g.rect(at.x - HANDLE / 2, at.y - HANDLE / 2, HANDLE, HANDLE);
      g.fill(0xffffff).stroke({ width: 1.5, color: ACCENT });
    }
    if (this.marquee) {
      const r = this.screenRect(this.marquee);
      g.rect(r.x, r.y, r.w, r.h).fill({ color: ACCENT, alpha: 0.08 }).stroke({ width: 1, color: ACCENT });
    }
    if (this.penPreview && this.penPreview.length > 1) {
      const pts = this.penPreview.map((p) => this.camera.toScreen(p));
      g.moveTo(pts[0]!.x, pts[0]!.y);
      for (const p of pts.slice(1)) g.lineTo(p.x, p.y);
      g.stroke({ width: 3 * this.camera.zoom, color: 0x1f2328, cap: "round", join: "round" });
    }
    this.drawCursors();
  }

  private drawCursors(): void {
    for (const [id, view] of this.cursorViews) {
      if (!this.presence.others.has(id)) {
        view.g.destroy();
        view.label.destroy({ children: true });
        this.cursorViews.delete(id);
      }
    }
    for (const [id, pos] of this.presence.others) {
      let view = this.cursorViews.get(id);
      if (!view) {
        const color = cursorColor(id);
        const g = new Graphics().poly([0, 0, 0, 16, 4.5, 12, 8, 19, 10.5, 18, 7, 11, 12.5, 11]).fill(color).stroke({ width: 1, color: 0xffffff });
        const name = new Text({ text: cursorName(id), style: { fontSize: 11, fill: 0xffffff, fontFamily: "system-ui, sans-serif" } });
        const bg = new Graphics().roundRect(-4, -2, name.width + 8, name.height + 4, 4).fill(color);
        const label = new Container();
        label.addChild(bg, name); // background first, so it is drawn under the name
        this.cursorLayer.addChild(g, label);
        view = { g, label };
        this.cursorViews.set(id, view);
      }
      const s = this.camera.toScreen(pos);
      view.g.position.set(s.x, s.y);
      view.label.position.set(s.x + 14, s.y + 18);
    }
  }
}
