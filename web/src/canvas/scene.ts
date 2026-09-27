import { Container } from "pixi.js";
import { ShapeType } from "../gen/whiteboard/v1/protocol_pb";
import { isVisible } from "../sync/doc";
import type { SyncSession } from "../sync/session";
import type { Camera } from "./camera";
import type { Editor } from "./editor";
import { rectInside, type Point, type Rect } from "./geometry";
import { createView, destroyView, drawView, type View } from "./render";
import { bindingIds, geometry, hitTest, type Geometry } from "./shapes";

interface Entry {
  view: View;
  z: string;
  geom: Geometry;
}

/**
 * Draws the session's visible objects in board coordinates, one view per
 * object, in (z, id) order. Arrows attached to an object are redrawn when it
 * changes.
 */
export class Scene {
  readonly world = new Container();
  private readonly entries = new Map<string, Entry>();
  /** Paint order, bottom to top. */
  private order: string[] = [];
  /** target id -> arrows attached to it */
  private readonly dependents = new Map<string, Set<string>>();
  private readonly arrowTargets = new Map<string, string[]>();
  private textResolution = window.devicePixelRatio || 1;
  private resolutionTimer: ReturnType<typeof setTimeout> | undefined;

  constructor(
    private readonly session: SyncSession,
    private readonly camera: Camera,
    private readonly editor: Editor,
    private readonly requestRender: () => void,
  ) {
    camera.subscribe(() => this.applyCamera());
    this.applyCamera();
    // Hide an object's text while it is edited in place, and show it again after.
    let editing: string | null = null;
    editor.subscribe(() => {
      if (editor.editing === editing) return;
      const ids = new Set([editing, editor.editing].filter((x): x is string => x !== null));
      editing = editor.editing;
      this.update(ids);
    });
  }

  /** Redraws the given objects (and arrows attached to them), or everything when null. */
  update(changed: ReadonlySet<string> | null): void {
    const ids = new Set(changed ?? [...this.entries.keys(), ...[...this.session.doc.values()].map((o) => o.id)]);
    for (const id of [...ids]) for (const dep of this.dependents.get(id) ?? []) ids.add(dep);

    let reorder = false;
    for (const id of ids) {
      const o = this.session.doc.get(id);
      let e = this.entries.get(id);
      if (!o || !isVisible(o)) {
        if (e) {
          destroyView(e.view);
          this.entries.delete(id);
          reorder = true;
        }
        this.setArrowTargets(id, []);
        continue;
      }
      const geom = geometry(this.session.doc, o);
      if (!e) {
        e = { view: createView(), z: "", geom };
        this.world.addChild(e.view.root);
        this.entries.set(id, e);
        reorder = true;
      }
      const z = o.props.z ?? "";
      if (e.z !== z) reorder = true;
      e.z = z;
      e.geom = geom;
      drawView(e.view, o.props, geom, this.textResolution, this.editor.editing === id);
      this.setArrowTargets(id, o.props.type === ShapeType.ARROW ? bindingIds(o.props) : []);
    }
    if (reorder) this.reorder();
    this.requestRender();
  }

  geometryOf(id: string): Geometry | undefined {
    return this.entries.get(id)?.geom;
  }

  /** The topmost object under a board point; tolerance is in screen pixels. */
  hitTest(p: Point, tolerancePx = 6, filter?: (id: string) => boolean): string | null {
    const tol = tolerancePx / this.camera.zoom;
    for (let i = this.order.length - 1; i >= 0; i--) {
      const id = this.order[i]!;
      if (filter && !filter(id)) continue;
      if (hitTest(this.entries.get(id)!.geom, p, tol)) return id;
    }
    return null;
  }

  /** Objects entirely inside a board rectangle, for marquee selection. */
  inside(r: Rect): string[] {
    return this.order.filter((id) => rectInside(this.entries.get(id)!.geom.bounds, r));
  }

  all(): readonly string[] {
    return this.order;
  }

  /** The highest z in use, for placing new objects on top. */
  maxZ(): string | null {
    let max: string | null = null;
    for (const e of this.entries.values()) if (e.z && (max === null || e.z > max)) max = e.z;
    return max;
  }

  private setArrowTargets(arrow: string, targets: string[]): void {
    for (const t of this.arrowTargets.get(arrow) ?? []) this.dependents.get(t)?.delete(arrow);
    if (targets.length === 0) {
      this.arrowTargets.delete(arrow);
      return;
    }
    this.arrowTargets.set(arrow, targets);
    for (const t of targets) {
      let s = this.dependents.get(t);
      if (!s) this.dependents.set(t, (s = new Set()));
      s.add(arrow);
    }
  }

  private reorder(): void {
    this.order = [...this.entries.entries()]
      .sort(([ida, a], [idb, b]) => (a.z < b.z ? -1 : a.z > b.z ? 1 : ida < idb ? -1 : ida > idb ? 1 : 0))
      .map(([id]) => id);
    this.order.forEach((id, i) => this.world.setChildIndex(this.entries.get(id)!.view.root, i));
  }

  private applyCamera(): void {
    const { x, y, zoom } = this.camera;
    this.world.position.set(-x * zoom, -y * zoom);
    this.world.scale.set(zoom);
    // Re-rasterize text for the new zoom once zooming pauses, so it stays sharp.
    clearTimeout(this.resolutionTimer);
    this.resolutionTimer = setTimeout(() => {
      const res = (window.devicePixelRatio || 1) * Math.min(4, Math.max(0.5, zoom));
      if (Math.abs(res - this.textResolution) < 0.01) return;
      this.textResolution = res;
      for (const e of this.entries.values()) if (e.view.text) e.view.text.resolution = res;
      this.requestRender();
    }, 150);
  }
}
