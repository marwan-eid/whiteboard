import { create } from "@bufbuild/protobuf";
import { generateKeyBetween } from "fractional-indexing";
import type { Application, FederatedPointerEvent } from "pixi.js";
import { OpSchema, ShapeType } from "../gen/whiteboard/v1/protocol_pb";
import type { SyncSession } from "../sync/session";
import type { Scene } from "./scene";

const DOUBLE_CLICK_MS = 350;
const DOUBLE_CLICK_PX = 6;
const NEW_SIZE = { w: 160, h: 100 };
const PALETTE = [0xfff3b0ff, 0xcdeac0ff, 0xbde0feff, 0xffc8ddff, 0xe2d6ffff, 0xffd6a5ff];

/**
 * Minimal editing for the sync milestone: double-click empty space to add a
 * shape, drag to move, Delete to remove. Drags send at most one batch per
 * animation frame.
 */
export class Interaction {
  private lastDown: { t: number; x: number; y: number } | null = null;
  private drag: { id: string; dx: number; dy: number } | null = null;
  private pendingMove: { x: number; y: number } | null = null;
  private frameRequested = false;

  constructor(
    private readonly app: Application,
    private readonly session: SyncSession,
    private readonly scene: Scene,
  ) {
    app.stage.eventMode = "static";
    app.stage.hitArea = app.screen;
    app.stage.on("pointerdown", (e) => this.onStagePointerDown(e));
    app.stage.on("globalpointermove", (e) => this.onPointerMove(e));
    app.stage.on("pointerup", () => this.endDrag());
    app.stage.on("pointerupoutside", () => this.endDrag());
    window.addEventListener("keydown", (e) => this.onKeyDown(e));
  }

  /** Called by the scene when a shape is pressed. */
  onShapePointerDown(id: string, e: FederatedPointerEvent): void {
    e.stopPropagation();
    const p = this.session.doc.get(id)?.props;
    if (!p) return;
    this.scene.select(id);
    this.drag = { id, dx: e.global.x - (p.x ?? 0), dy: e.global.y - (p.y ?? 0) };
  }

  private onStagePointerDown(e: FederatedPointerEvent): void {
    if (e.target !== this.app.stage) return;
    this.scene.select(null);
    const now = performance.now();
    const { x, y } = e.global;
    const prev = this.lastDown;
    this.lastDown = { t: now, x, y };
    if (prev && now - prev.t < DOUBLE_CLICK_MS && Math.hypot(x - prev.x, y - prev.y) < DOUBLE_CLICK_PX) {
      this.lastDown = null;
      this.createAt(x, y);
    }
  }

  private createAt(x: number, y: number): void {
    const id = this.session.newObjectId();
    this.session.edit([
      create(OpSchema, {
        id,
        props: {
          type: ShapeType.RECT,
          x: Math.round(x - NEW_SIZE.w / 2),
          y: Math.round(y - NEW_SIZE.h / 2),
          w: NEW_SIZE.w,
          h: NEW_SIZE.h,
          z: generateKeyBetween(this.scene.maxZ(), null),
          fill: PALETTE[Math.floor(Math.random() * PALETTE.length)]!,
          stroke: 0x1f2328ff,
          strokeWidth: 1.5,
        },
      }),
    ]);
    this.scene.select(id);
  }

  private onPointerMove(e: FederatedPointerEvent): void {
    if (!this.drag) return;
    this.pendingMove = { x: Math.round(e.global.x - this.drag.dx), y: Math.round(e.global.y - this.drag.dy) };
    if (!this.frameRequested) {
      this.frameRequested = true;
      requestAnimationFrame(() => {
        this.frameRequested = false;
        this.flushMove();
      });
    }
  }

  private endDrag(): void {
    this.flushMove();
    this.drag = null;
  }

  private flushMove(): void {
    if (!this.drag || !this.pendingMove) return;
    this.session.edit([create(OpSchema, { id: this.drag.id, props: this.pendingMove })]);
    this.pendingMove = null;
  }

  private onKeyDown(e: KeyboardEvent): void {
    const id = this.scene.selected;
    if (!id || (e.key !== "Delete" && e.key !== "Backspace")) return;
    this.session.edit([create(OpSchema, { id, props: { deleted: true } })]);
    this.scene.select(null);
  }
}
