import { create, isFieldSet, type DescField } from "@bufbuild/protobuf";
import { ObjectPropsSchema, OpSchema, type ObjectProps, type Op } from "../gen/whiteboard/v1/protocol_pb";
import type { SyncSession } from "./session";

type Values = Map<string, unknown>; // field localName -> value
type PropRecord = Record<string, unknown>;

const FIELDS: readonly DescField[] = ObjectPropsSchema.fields;

// When a field had no value before, undo writes this instead (a register
// cannot be "unset"). Fields not listed are left alone on undo.
const EMPTY: Record<string, unknown> = { deleted: false, text: "" };

interface Entry {
  undo: Op[];
  redo: Op[];
}

/**
 * Local undo/redo (ADR-0001): undoing writes the previous values back with a
 * fresh stamp, so it wins over older edits and syncs like any other edit.
 * Only this client's own actions are undoable.
 */
export class History {
  private undoStack: Entry[] = [];
  private redoStack: Entry[] = [];
  private readonly listeners = new Set<() => void>();

  constructor(
    private readonly session: SyncSession,
    private readonly limit = 200,
  ) {}

  get canUndo(): boolean {
    return this.undoStack.length > 0;
  }

  get canRedo(): boolean {
    return this.redoStack.length > 0;
  }

  /** Applies ops as one undoable step. */
  apply(ops: Op[]): void {
    const g = this.begin();
    g.edit(ops);
    g.end();
  }

  /** Starts a multi-edit gesture (a drag) that undoes as one step. */
  begin(): Gesture {
    return new Gesture(this.session, (e) => this.push(e));
  }

  undo(): boolean {
    const e = this.undoStack.pop();
    if (!e) return false;
    this.session.edit(e.undo);
    this.redoStack.push(e);
    this.changed();
    return true;
  }

  redo(): boolean {
    const e = this.redoStack.pop();
    if (!e) return false;
    this.session.edit(e.redo);
    this.undoStack.push(e);
    this.changed();
    return true;
  }

  subscribe(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  private push(e: Entry): void {
    this.undoStack.push(e);
    if (this.undoStack.length > this.limit) this.undoStack.shift();
    this.redoStack = [];
    this.changed();
  }

  private changed(): void {
    for (const fn of this.listeners) fn();
  }
}

export class Gesture {
  private readonly before = new Map<string, Values>();
  private readonly after = new Map<string, Values>();
  private readonly created = new Set<string>();

  constructor(
    private readonly session: SyncSession,
    private readonly onEnd: (e: Entry) => void,
  ) {}

  /** Applies ops now, remembering each field's value from before the gesture. */
  edit(ops: Op[]): void {
    for (const op of ops) {
      if (!op.props) continue;
      const cur = this.session.doc.get(op.id);
      if (!cur || cur.props.type === undefined) this.created.add(op.id);
      const before = this.before.get(op.id) ?? new Map<string, unknown>();
      const after = this.after.get(op.id) ?? new Map<string, unknown>();
      for (const f of FIELDS) {
        if (!isFieldSet(op.props, f)) continue;
        if (!before.has(f.localName)) {
          before.set(f.localName, cur && isFieldSet(cur.props, f) ? (cur.props as unknown as PropRecord)[f.localName] : undefined);
        }
        after.set(f.localName, (op.props as unknown as PropRecord)[f.localName]);
      }
      this.before.set(op.id, before);
      this.after.set(op.id, after);
    }
    this.session.edit(ops);
  }

  /** Records the gesture as one undo step (if it changed anything). */
  end(): void {
    const undo: Op[] = [];
    const redo: Op[] = [];
    for (const [id, after] of this.after) {
      if (this.created.has(id)) {
        // Undoing a create deletes; redoing it restores (type is fixed at create).
        undo.push(op(id, new Map([["deleted", true]])));
        const values = new Map(after);
        values.delete("type");
        values.set("deleted", false);
        redo.push(op(id, values));
        continue;
      }
      const prev = new Map<string, unknown>();
      for (const [k, v] of this.before.get(id)!) {
        if (v !== undefined) prev.set(k, v);
        else if (k in EMPTY) prev.set(k, EMPTY[k]);
      }
      if (prev.size) undo.push(op(id, prev));
      redo.push(op(id, after));
    }
    if (undo.length) this.onEnd({ undo, redo });
  }

  /** Reverts the gesture's edits without recording it (Escape mid-drag). */
  cancel(): void {
    const undo: Op[] = [];
    for (const [id, before] of this.before) {
      if (this.created.has(id)) {
        undo.push(op(id, new Map([["deleted", true]])));
        continue;
      }
      const prev = new Map([...before].filter(([, v]) => v !== undefined));
      if (prev.size) undo.push(op(id, prev));
    }
    if (undo.length) this.session.edit(undo);
  }
}

function op(id: string, values: Values): Op {
  const props: ObjectProps = create(ObjectPropsSchema, {});
  for (const [k, v] of values) (props as unknown as PropRecord)[k] = v;
  return create(OpSchema, { id, props });
}
