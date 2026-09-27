import { CanvasTextMetrics, Container, Graphics, Text, TextStyle } from "pixi.js";
import { ShapeType, type ObjectProps } from "../gen/whiteboard/v1/protocol_pb";
import {
  DEFAULT_STROKE,
  STICKY_FILL,
  STICKY_FONT_SIZE,
  TEXT_FONT_SIZE,
  TEXT_PADDING,
  canHoldText,
  type Geometry,
} from "./shapes";

const FONT_FAMILY = "system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif";
const TEXT_COLOR = 0x1f2328;

/** The display objects for one board object. */
export interface View {
  root: Container;
  g: Graphics;
  text?: Text;
  /** What the text's style was built from, to skip re-layout when unchanged. */
  textStyleKey?: string;
}

export function createView(): View {
  const root = new Container();
  const g = new Graphics();
  root.addChild(g);
  return { root, g };
}

export function destroyView(v: View): void {
  v.root.destroy({ children: true });
}

/** 0xRRGGBBAA to Pixi's color and alpha. */
export function rgba(v: number): { color: number; alpha: number } {
  return { color: v >>> 8, alpha: (v & 0xff) / 255 };
}

export function fontSize(p: ObjectProps): number {
  return p.fontSize ?? (p.type === ShapeType.STICKY ? STICKY_FONT_SIZE : TEXT_FONT_SIZE);
}

export function textStyle(p: ObjectProps, wrapWidth: number): TextStyle {
  return new TextStyle({
    fontFamily: FONT_FAMILY,
    fontSize: fontSize(p),
    fill: p.type === ShapeType.TEXT ? rgba(p.stroke ?? DEFAULT_STROKE).color : TEXT_COLOR,
    wordWrap: true,
    breakWords: true,
    wordWrapWidth: Math.max(1, wrapWidth),
    lineHeight: fontSize(p) * 1.3,
  });
}

/** Height a text object needs for its content at its width. */
export function measureTextHeight(p: ObjectProps, text: string): number {
  const w = (p.w ?? 0) - 2 * TEXT_PADDING;
  const m = CanvasTextMetrics.measureText(text || " ", textStyle(p, w));
  return Math.ceil(m.height + 2 * TEXT_PADDING);
}

/** Redraws a view; hideText is set while the text is being edited in place. */
export function drawView(v: View, p: ObjectProps, geom: Geometry, textResolution: number, hideText: boolean): void {
  const g = v.g;
  const b = geom.bounds;
  const strokeWidth = p.strokeWidth ?? (geom.type === ShapeType.FREEHAND ? 3 : 1.5);
  const stroke = { width: strokeWidth, ...rgba(p.stroke ?? DEFAULT_STROKE), cap: "round" as const, join: "round" as const };
  g.clear();

  switch (geom.type) {
    case ShapeType.ELLIPSE:
      g.ellipse(b.x + b.w / 2, b.y + b.h / 2, b.w / 2, b.h / 2).fill(rgba(p.fill ?? 0xffffffff)).stroke(stroke);
      break;
    case ShapeType.STICKY:
      g.rect(b.x + 3, b.y + 4, b.w, b.h).fill({ color: 0x000000, alpha: 0.08 });
      g.rect(b.x, b.y, b.w, b.h).fill(rgba(p.fill ?? STICKY_FILL));
      break;
    case ShapeType.TEXT:
      break;
    case ShapeType.ARROW: {
      const line = geom.line;
      if (!line || line.length < 2) break;
      const [s, e] = [line[0]!, line[line.length - 1]!];
      g.moveTo(s.x, s.y).lineTo(e.x, e.y).stroke(stroke);
      const angle = Math.atan2(e.y - s.y, e.x - s.x);
      const len = 10 + strokeWidth * 2;
      for (const side of [-1, 1]) {
        const a = angle + Math.PI - (side * Math.PI) / 7;
        g.moveTo(e.x, e.y).lineTo(e.x + Math.cos(a) * len, e.y + Math.sin(a) * len);
      }
      g.stroke(stroke);
      break;
    }
    case ShapeType.FREEHAND: {
      const line = geom.line ?? [];
      if (line.length === 1) {
        g.circle(line[0]!.x, line[0]!.y, strokeWidth / 2).fill(rgba(p.stroke ?? DEFAULT_STROKE));
      } else if (line.length > 1) {
        g.moveTo(line[0]!.x, line[0]!.y);
        for (const q of line.slice(1)) g.lineTo(q.x, q.y);
        g.stroke(stroke);
      }
      break;
    }
    default:
      g.rect(b.x, b.y, b.w, b.h).fill(rgba(p.fill ?? 0xffffffff)).stroke(stroke);
  }

  const text = canHoldText(geom.type) && !hideText ? (p.text ?? "") : "";
  if (text) {
    if (!v.text) {
      v.text = new Text({ text: "" });
      v.root.addChild(v.text);
    }
    const wrap = b.w - 2 * TEXT_PADDING;
    const key = `${fontSize(p)}|${wrap}|${p.stroke ?? ""}|${p.type ?? ""}`;
    if (v.textStyleKey !== key) {
      v.text.style = textStyle(p, wrap);
      v.textStyleKey = key;
    }
    if (v.text.text !== text) v.text.text = text;
    if (v.text.resolution !== textResolution) v.text.resolution = textResolution;
    v.text.position.set(b.x + TEXT_PADDING, b.y + TEXT_PADDING);
  } else if (v.text) {
    v.text.destroy();
    v.text = undefined;
    v.textStyleKey = undefined;
  }
}
