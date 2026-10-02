import { Container, Particle, ParticleContainer, Texture } from "pixi.js";
import { ShapeType } from "../gen/whiteboard/v1/protocol_pb";
import { isShape, type Doc } from "../sync/doc";
import type { SyncSession } from "../sync/session";
import type { Camera } from "./camera";
import type { Editor } from "./editor";
import type { FrameScheduler } from "./frames";
import { rectInside, type Point, type Rect } from "./geometry";
import { createView, destroyView, drawView, rgba, type View } from "./render";
import { bindingIds, geometry, hitTest, type Geometry } from "./shapes";

interface Entry {
  /** Absent in LOD mode, where everything is drawn in one Graphics. */
  view?: View;
  z: string;
  geom: Geometry;
}

const LOD_FALLBACK_FILL = 0xd0d7deff;

/**
 * Draws the session's visible objects in board coordinates in (z, id)
 * order. Normally each object has its own view, and arrows attached to an
 * object are redrawn when it changes. In LOD mode (zoomed far out) every
 * object is one particle (a scaled, tinted white quad) in a ParticleContainer,
 * updated incrementally, so tens of thousands of objects stay cheap to pan,
 * zoom and stream in.
 */
export class Scene {
  readonly world = new Container();
  private readonly entries = new Map<string, Entry>();
  /** Paint order, bottom to top. */
  private order: string[] = [];
  private orderDirty = false;
  /** target id -> arrows attached to it */
  private readonly dependents = new Map<string, Set<string>>();
  private readonly arrowTargets = new Map<string, string[]>();
  private textResolution = window.devicePixelRatio || 1;
  private resolutionTimer: ReturnType<typeof setTimeout> | undefined;
  private lod = false;
  private readonly lodLayer = new ParticleContainer({ dynamicProperties: { position: false, vertex: false, rotation: false, uvs: false, color: false } });
  private readonly particles = new Map<string, Particle>();
  /** Particles were removed; rebuild the container's list once, before rendering. */
  private particlesRemoved = false;
  /** Particles changed; upload them to the GPU before the next render. Panning
   * alone moves the whole layer and needs no upload. */
  private particlesChanged = false;

  constructor(
    private readonly session: SyncSession,
    private readonly camera: Camera,
    private readonly editor: Editor,
    private readonly frames: FrameScheduler,
  ) {
    camera.subscribe(() => this.applyCamera());
    this.applyCamera();
    frames.beforeRender(() => this.beforeRender());
    // Hide an object's text while it is edited in place, and show it again after.
    let editing: string | null = null;
    editor.subscribe(() => {
      if (editor.editing === editing) return;
      const ids = new Set([editing, editor.editing].filter((x): x is string => x !== null));
      editing = editor.editing;
      this.update(ids);
    });
  }

  /** The document drawn: a history version when set, otherwise the live one. */
  private override: Doc | null = null;

  private get doc(): Doc {
    return this.override ?? this.session.doc;
  }

  /** Shows another document (a past version), or the live one again with null. */
  setOverride(d: Doc | null): void {
    this.override = d;
    this.update(null);
  }

  get isLOD(): boolean {
    return this.lod;
  }

  /** Switches between per-object views and LOD boxes. */
  setLOD(on: boolean): void {
    if (on === this.lod) return;
    this.lod = on;
    for (const e of this.entries.values()) {
      if (e.view) destroyView(e.view);
      e.view = undefined;
    }
    if (on) {
      this.world.addChild(this.lodLayer);
      this.update(null);
    } else {
      this.world.removeChild(this.lodLayer);
      this.particles.clear();
      this.lodLayer.particleChildren.length = 0;
      this.update(null);
    }
  }

  /** Redraws the given objects (and arrows attached to them), or everything when null. */
  update(changed: ReadonlySet<string> | null): void {
    const ids = new Set(changed ?? [...this.entries.keys(), ...[...this.doc.values()].map((o) => o.id)]);
    for (const id of [...ids]) for (const dep of this.dependents.get(id) ?? []) ids.add(dep);

    for (const id of ids) {
      const o = this.doc.get(id);
      let e = this.entries.get(id);
      if (!o || !isShape(o)) {
        if (e) {
          if (e.view) destroyView(e.view);
          this.entries.delete(id);
          this.orderDirty = true;
        }
        if (this.particles.delete(id)) this.particlesRemoved = true;
        this.setArrowTargets(id, []);
        continue;
      }
      const geom = geometry(this.doc, o);
      if (!e) {
        e = { z: "", geom };
        this.entries.set(id, e);
        this.orderDirty = true;
      }
      const z = o.props.z ?? "";
      if (e.z !== z) this.orderDirty = true;
      e.z = z;
      e.geom = geom;
      this.setArrowTargets(id, o.props.type === ShapeType.ARROW ? bindingIds(o.props) : []);
      if (this.lod) {
        this.placeParticle(id, geom.bounds, o.props.fill);
        continue;
      }
      if (!e.view) {
        e.view = createView();
        this.world.addChild(e.view.root);
        this.orderDirty = true;
      }
      drawView(e.view, o.props, geom, this.textResolution, this.editor.editing === id);
    }
    if (!this.lod && this.orderDirty) this.reorder();
    this.frames.request();
  }

  geometryOf(id: string): Geometry | undefined {
    return this.entries.get(id)?.geom;
  }

  /** The topmost object under a board point; tolerance is in screen pixels. */
  hitTest(p: Point, tolerancePx = 6, filter?: (id: string) => boolean): string | null {
    const tol = tolerancePx / this.camera.zoom;
    const order = this.paintOrder();
    for (let i = order.length - 1; i >= 0; i--) {
      const id = order[i]!;
      if (filter && !filter(id)) continue;
      if (hitTest(this.entries.get(id)!.geom, p, tol)) return id;
    }
    return null;
  }

  /** Objects entirely inside a board rectangle, for marquee selection. */
  inside(r: Rect): string[] {
    return this.paintOrder().filter((id) => rectInside(this.entries.get(id)!.geom.bounds, r));
  }

  all(): readonly string[] {
    return this.paintOrder();
  }

  /** Arrows attached to an object. */
  arrowsAttachedTo(id: string): ReadonlySet<string> {
    return this.dependents.get(id) ?? new Set();
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

  private paintOrder(): string[] {
    if (this.orderDirty) this.reorder();
    return this.order;
  }

  private reorder(): void {
    this.orderDirty = false;
    this.order = [...this.entries.entries()]
      .sort(([ida, a], [idb, b]) => (a.z < b.z ? -1 : a.z > b.z ? 1 : ida < idb ? -1 : ida > idb ? 1 : 0))
      .map(([id]) => id);
    if (this.lod) return;
    let i = 0;
    for (const id of this.order) {
      const view = this.entries.get(id)!.view;
      if (view) this.world.setChildIndex(view.root, i++);
    }
  }

  private placeParticle(id: string, r: Rect, fill: number | undefined): void {
    let p = this.particles.get(id);
    if (!p) {
      p = new Particle({ texture: Texture.WHITE });
      this.particles.set(id, p);
      this.lodLayer.particleChildren.push(p);
    }
    const { color, alpha } = rgba(fill ?? LOD_FALLBACK_FILL);
    p.x = r.x;
    p.y = r.y;
    p.scaleX = Math.max(1, r.w);
    p.scaleY = Math.max(1, r.h);
    p.tint = color;
    p.alpha = alpha || 0.35; // unfilled shapes still show as faint boxes
    this.particlesChanged = true;
  }

  private beforeRender(): void {
    if (!this.lod) return;
    if (this.particlesRemoved) {
      this.particlesRemoved = false;
      this.particlesChanged = true;
      this.lodLayer.particleChildren = [...this.particles.values()];
    }
    if (this.particlesChanged) {
      this.particlesChanged = false;
      this.lodLayer.update();
    }
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
      for (const e of this.entries.values()) if (e.view?.text) e.view.text.resolution = res;
      this.frames.request();
    }, 150);
  }
}
