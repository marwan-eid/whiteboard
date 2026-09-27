import { create } from "@bufbuild/protobuf";
import { OpSchema, ShapeType } from "../gen/whiteboard/v1/protocol_pb";
import type { History } from "../sync/history";
import type { SyncSession } from "../sync/session";
import type { Camera } from "./camera";
import type { Editor } from "./editor";
import { fontSize, measureTextHeight } from "./render";
import { TEXT_PADDING, propRect } from "./shapes";

/**
 * Edits a sticky or text object's text in place with a textarea laid over
 * the canvas. The text is committed as one undoable edit when editing ends.
 */
export class TextEditor {
  private el: HTMLTextAreaElement | null = null;
  private id: string | null = null;
  private original = "";

  constructor(
    private readonly host: HTMLElement,
    private readonly session: SyncSession,
    private readonly history: History,
    private readonly camera: Camera,
    private readonly editor: Editor,
  ) {
    camera.subscribe(() => this.position());
  }

  get active(): boolean {
    return this.el !== null;
  }

  open(id: string): void {
    this.close();
    const o = this.session.doc.get(id);
    if (!o) return;
    this.id = id;
    this.original = o.props.text ?? "";
    const el = document.createElement("textarea");
    el.className = "text-editor";
    el.value = this.original;
    el.spellcheck = false;
    el.addEventListener("keydown", (e) => {
      e.stopPropagation(); // keep canvas shortcuts (Delete, V, ...) out of the text
      if (e.key === "Escape" || (e.key === "Enter" && (e.ctrlKey || e.metaKey))) {
        e.preventDefault();
        this.close();
      }
    });
    el.addEventListener("input", () => this.position());
    el.addEventListener("blur", () => this.close());
    this.el = el;
    this.host.appendChild(el);
    this.editor.setEditing(id);
    this.position();
    el.focus();
    el.select();
  }

  /** Commits the text (if it changed) and removes the textarea. */
  close(): void {
    const el = this.el;
    const id = this.id;
    if (!el || !id) return;
    this.el = null;
    this.id = null;
    el.remove();
    this.editor.setEditing(null);

    const o = this.session.doc.get(id);
    if (!o) return;
    const text = el.value;
    if (o.props.type === ShapeType.TEXT && text.trim() === "") {
      this.history.apply([create(OpSchema, { id, props: { deleted: true } })]);
      return;
    }
    if (text === this.original) return;
    const props: { text: string; h?: number } = { text };
    if (o.props.type === ShapeType.TEXT) props.h = measureTextHeight(o.props, text);
    this.history.apply([create(OpSchema, { id, props })]);
  }

  private position(): void {
    const el = this.el;
    const o = this.id ? this.session.doc.get(this.id) : undefined;
    if (!el || !o) return;
    const z = this.camera.zoom;
    const r = propRect(o.props);
    const tl = this.camera.toScreen(r);
    const size = fontSize(o.props) * z;
    Object.assign(el.style, {
      left: `${tl.x + TEXT_PADDING * z}px`,
      top: `${tl.y + TEXT_PADDING * z}px`,
      width: `${Math.max(20, (r.w - 2 * TEXT_PADDING) * z)}px`,
      fontSize: `${size}px`,
      lineHeight: `${size * 1.3}px`,
      height: "auto",
    });
    const minHeight = o.props.type === ShapeType.STICKY ? (r.h - 2 * TEXT_PADDING) * z : size * 1.3;
    el.style.height = `${Math.max(minHeight, el.scrollHeight)}px`;
  }
}
