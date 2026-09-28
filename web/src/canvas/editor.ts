export type ToolId = "select" | "hand" | "rect" | "ellipse" | "sticky" | "text" | "arrow" | "pen";

export const TOOLS: readonly { id: ToolId; label: string; key: string }[] = [
  { id: "select", label: "Select", key: "v" },
  { id: "hand", label: "Hand", key: "h" },
  { id: "rect", label: "Rectangle", key: "r" },
  { id: "ellipse", label: "Ellipse", key: "o" },
  { id: "sticky", label: "Sticky note", key: "s" },
  { id: "text", label: "Text", key: "t" },
  { id: "arrow", label: "Arrow", key: "a" },
  { id: "pen", label: "Pen", key: "p" },
];

/** UI state shared by the canvas, tools and toolbar. */
export class Editor {
  readonly selection = new Set<string>();
  private _tool: ToolId = "select";
  private _editing: string | null = null;
  private _historyMode = false;
  private _viewOnly = false;
  private readonly listeners = new Set<() => void>();

  get tool(): ToolId {
    return this._tool;
  }

  /** The object whose text is being edited, if any. */
  get editing(): string | null {
    return this._editing;
  }

  /** The canvas can be panned and zoomed but not edited (history mode or view-only access). */
  get readOnly(): boolean {
    return this._historyMode || this._viewOnly;
  }

  /** View-only access: this client may not change the board. */
  get viewOnly(): boolean {
    return this._viewOnly;
  }

  setReadOnly(on: boolean): void {
    this._historyMode = on;
    this.readOnlyChanged();
  }

  setViewOnly(on: boolean): void {
    this._viewOnly = on;
    this.readOnlyChanged();
  }

  private readOnlyChanged(): void {
    if (this.readOnly) {
      this.selection.clear();
      this._tool = "select";
    }
    this.changed();
  }

  setTool(t: ToolId): void {
    this._tool = t;
    if (t !== "select") this.selection.clear();
    this.changed();
  }

  select(ids: Iterable<string>, additive = false): void {
    if (!additive) this.selection.clear();
    for (const id of ids) this.selection.add(id);
    this.changed();
  }

  toggle(id: string): void {
    if (!this.selection.delete(id)) this.selection.add(id);
    this.changed();
  }

  clearSelection(): void {
    if (this.selection.size === 0) return;
    this.selection.clear();
    this.changed();
  }

  setEditing(id: string | null): void {
    this._editing = id;
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
