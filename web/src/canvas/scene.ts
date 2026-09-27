import { Container, Graphics, type FederatedPointerEvent } from "pixi.js";
import { ShapeType, type ObjectProps } from "../gen/whiteboard/v1/protocol_pb";
import { isVisible } from "../sync/doc";
import type { SyncSession } from "../sync/session";

const SELECTION_COLOR = 0x0969da;

/** Draws the session's visible objects, keeping one Graphics per object. */
export class Scene {
  readonly layer = new Container();
  private readonly shapes = new Map<string, { g: Graphics; z: string }>();
  private _selected: string | null = null;

  constructor(
    private readonly session: SyncSession,
    private readonly onShapePointerDown: (id: string, e: FederatedPointerEvent) => void,
  ) {}

  get selected(): string | null {
    return this._selected;
  }

  select(id: string | null): void {
    const prev = this._selected;
    this._selected = id;
    this.update(new Set([prev, id].filter((x): x is string => x !== null)));
  }

  /** Redraws the given objects, or everything when changed is null. */
  update(changed: ReadonlySet<string> | null): void {
    const ids = changed ?? new Set([...this.shapes.keys(), ...[...this.session.doc.values()].map((o) => o.id)]);
    let reorder = false;
    for (const id of ids) {
      const o = this.session.doc.get(id);
      let entry = this.shapes.get(id);
      if (!o || !isVisible(o)) {
        if (entry) {
          entry.g.destroy();
          this.shapes.delete(id);
        }
        if (this._selected === id) this._selected = null;
        continue;
      }
      if (!entry) {
        const g = new Graphics();
        g.eventMode = "static";
        g.cursor = "move";
        g.on("pointerdown", (e: FederatedPointerEvent) => this.onShapePointerDown(id, e));
        this.layer.addChild(g);
        entry = { g, z: "" };
        this.shapes.set(id, entry);
      }
      const z = o.props.z ?? "";
      if (entry.z !== z || entry.g.parent === null) reorder = true;
      entry.z = z;
      draw(entry.g, o.props, id === this._selected);
    }
    if (reorder) this.reorder();
  }

  /** Paint order is (z, id), the same on every replica. */
  private reorder(): void {
    const sorted = [...this.shapes.entries()].sort(([ida, a], [idb, b]) =>
      a.z < b.z ? -1 : a.z > b.z ? 1 : ida < idb ? -1 : ida > idb ? 1 : 0,
    );
    sorted.forEach(([, { g }], i) => this.layer.setChildIndex(g, i));
  }

  /** The highest z in use, for placing new objects on top. */
  maxZ(): string | null {
    let max: string | null = null;
    for (const o of this.session.doc.values()) {
      const z = o.props.z;
      if (z !== undefined && (max === null || z > max)) max = z;
    }
    return max;
  }
}

function draw(g: Graphics, p: ObjectProps, selected: boolean): void {
  const w = p.w ?? 0;
  const h = p.h ?? 0;
  g.clear();
  if (p.type === ShapeType.ELLIPSE) g.ellipse(w / 2, h / 2, w / 2, h / 2);
  else g.rect(0, 0, w, h);
  g.fill(rgba(p.fill ?? 0xffffffff));
  g.stroke({ width: p.strokeWidth ?? 1, ...rgba(p.stroke ?? 0x1f2328ff) });
  if (selected) g.rect(-4, -4, w + 8, h + 8).stroke({ width: 2, color: SELECTION_COLOR });
  g.position.set(p.x ?? 0, p.y ?? 0);
}

/** 0xRRGGBBAA to Pixi's color + alpha. */
function rgba(v: number): { color: number; alpha: number } {
  return { color: v >>> 8, alpha: (v & 0xff) / 255 };
}
