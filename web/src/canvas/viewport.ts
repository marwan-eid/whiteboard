import type { ViewportRect } from "../net/protocol";
import type { Camera } from "./camera";
import type { Rect } from "./geometry";

/** Extra area subscribed around the visible one, as a fraction of its size per side. */
export const MARGIN = 0.5;
/** Below this zoom the client asks for boxes only (LOD). */
export const LOD_ZOOM = 0.2;

/**
 * Decides whether the subscribed region must change for the visible one:
 * when the view leaves it, when LOD turns on or off, or when the view has
 * shrunk so much (zooming in) that most of the subscription is wasted.
 */
export function needsResubscribe(sub: ViewportRect | undefined, visible: Rect, lod: boolean): boolean {
  if (!sub || sub.lod !== lod) return true;
  const inside =
    visible.x >= sub.x && visible.y >= sub.y && visible.x + visible.w <= sub.x + sub.w && visible.y + visible.h <= sub.y + sub.h;
  return !inside || visible.w * visible.h * 16 < sub.w * sub.h;
}

export function subscriptionFor(visible: Rect, lod: boolean): ViewportRect {
  const mx = visible.w * MARGIN;
  const my = visible.h * MARGIN;
  return { x: visible.x - mx, y: visible.y - my, w: visible.w + 2 * mx, h: visible.h + 2 * my, lod };
}

/**
 * Keeps the server subscription following the camera. Changes are sent at
 * most every intervalMs; the latest one always goes out.
 */
export class ViewportTracker {
  current: ViewportRect;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private readonly listeners = new Set<(v: ViewportRect) => void>();

  constructor(
    private readonly camera: Camera,
    private readonly size: () => { w: number; h: number },
    private readonly intervalMs = 80,
  ) {
    this.current = this.compute();
    camera.subscribe(() => this.check());
  }

  get lod(): boolean {
    return this.current.lod;
  }

  /** Re-checks after a resize or camera change. */
  check(): void {
    const { w, h } = this.size();
    const visible = this.camera.visible(w, h);
    if (!needsResubscribe(this.current, visible, this.camera.zoom < LOD_ZOOM)) return;
    if (this.timer !== undefined) return;
    this.timer = setTimeout(() => {
      this.timer = undefined;
      this.current = this.compute();
      for (const fn of this.listeners) fn(this.current);
    }, this.intervalMs);
  }

  subscribe(fn: (v: ViewportRect) => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  private compute(): ViewportRect {
    const { w, h } = this.size();
    return subscriptionFor(this.camera.visible(w, h), this.camera.zoom < LOD_ZOOM);
  }
}
