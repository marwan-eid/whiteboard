import type { Point, Rect } from "./geometry";

export const MIN_ZOOM = 0.02;
export const MAX_ZOOM = 8;

/**
 * Maps board coordinates to screen (CSS) pixels:
 * screen = (world - origin) * zoom, where origin is the world point at the
 * top-left corner of the viewport.
 */
export class Camera {
  x = 0;
  y = 0;
  zoom = 1;
  private readonly listeners = new Set<() => void>();

  toWorld(s: Point): Point {
    return { x: s.x / this.zoom + this.x, y: s.y / this.zoom + this.y };
  }

  toScreen(w: Point): Point {
    return { x: (w.x - this.x) * this.zoom, y: (w.y - this.y) * this.zoom };
  }

  /** The board region visible in a viewport of the given size. */
  visible(width: number, height: number): Rect {
    return { x: this.x, y: this.y, w: width / this.zoom, h: height / this.zoom };
  }

  panBy(dxScreen: number, dyScreen: number): void {
    this.x -= dxScreen / this.zoom;
    this.y -= dyScreen / this.zoom;
    this.changed();
  }

  /** Zooms by factor, keeping the world point under `at` (screen) fixed. */
  zoomAt(at: Point, factor: number): void {
    const anchor = this.toWorld(at);
    this.zoom = Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, this.zoom * factor));
    this.x = anchor.x - at.x / this.zoom;
    this.y = anchor.y - at.y / this.zoom;
    this.changed();
  }

  /** Centers the view on a world point at the given zoom. */
  centerOn(p: Point, zoom: number, width: number, height: number): void {
    this.zoom = Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, zoom));
    this.x = p.x - width / 2 / this.zoom;
    this.y = p.y - height / 2 / this.zoom;
    this.changed();
  }

  subscribe(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  private changed(): void {
    for (const fn of this.listeners) fn();
  }
}
