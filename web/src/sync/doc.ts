import { clone, create, isFieldSet, type DescField } from "@bufbuild/protobuf";
import {
  FieldStampsSchema,
  ObjectPropsSchema,
  ObjectStateSchema,
  type ObjectProps,
  type ObjectState,
  type Op,
} from "../gen/whiteboard/v1/protocol_pb";
import { compareStamps, stampFromProto, stampToProto, type Stamp } from "./stamp";

/**
 * The board's replicated data model: objects whose properties are
 * last-writer-wins registers ordered by Stamp. Mirrors internal/doc (Go);
 * testdata/lww-vectors.json keeps the two in agreement.
 */

const FIELDS: readonly DescField[] = ObjectPropsSchema.fields;
const FIELD_SLOTS = 32;

for (const f of FIELDS) {
  if (f.number >= FIELD_SLOTS) throw new Error(`ObjectProps field ${f.number} does not fit a 32-bit mask`);
}

type PropRecord = Record<string, unknown>;

export interface DocObject {
  readonly id: string;
  readonly props: ObjectProps;
  /** stamps[n] is the stamp of the write that set field number n. */
  readonly stamps: (Stamp | undefined)[];
}

/** Created (type set) and not deleted. */
export function isVisible(o: DocObject): boolean {
  return o.props.type !== undefined && o.props.deleted !== true;
}

export class Doc {
  private readonly objects = new Map<string, DocObject>();

  get size(): number {
    return this.objects.size;
  }

  get(id: string): DocObject | undefined {
    return this.objects.get(id);
  }

  values(): IterableIterator<DocObject> {
    return this.objects.values();
  }

  /** Merges one op written at stamp st; returns whether anything changed. */
  apply(op: Op, st: Stamp): boolean {
    const src = op.props;
    if (!src || !FIELDS.some((f) => isFieldSet(src, f))) return false;
    let o = this.objects.get(op.id);
    if (!o) {
      o = { id: op.id, props: create(ObjectPropsSchema), stamps: [] };
      this.objects.set(op.id, o);
    }
    let changed = false;
    for (const f of FIELDS) {
      if (!isFieldSet(src, f)) continue;
      const cur = o.stamps[f.number];
      if (cur && compareStamps(st, cur) <= 0) continue; // older, or this very write again
      o.stamps[f.number] = st;
      (o.props as unknown as PropRecord)[f.localName] = (src as unknown as PropRecord)[f.localName];
      changed = true;
    }
    return changed;
  }

  /** Applies every op with the batch stamp; returns the ids that changed. */
  applyBatch(ops: readonly Op[], st: Stamp): Set<string> {
    const changed = new Set<string>();
    for (const op of ops) {
      if (this.apply(op, st)) changed.add(op.id);
    }
    return changed;
  }

  /** Canonical form shared with the server: sorted by id, stamps grouped ascending. */
  snapshot(): ObjectState[] {
    return [...this.objects.keys()].sort(compareIds).map((id) => objectState(this.objects.get(id)!));
  }

  static fromSnapshot(states: readonly ObjectState[]): Doc {
    const d = new Doc();
    for (const s of states) {
      if (d.objects.has(s.id)) throw new Error(`duplicate object ${s.id} in snapshot`);
      const o: DocObject = { id: s.id, props: s.props ? clone(ObjectPropsSchema, s.props) : create(ObjectPropsSchema), stamps: [] };
      for (const fs of s.stamps) {
        const st = stampFromProto(fs.stamp);
        for (let n = 1; n < FIELD_SLOTS; n++) {
          if (fs.fieldMask & (1 << n)) o.stamps[n] = st;
        }
      }
      for (const f of FIELDS) {
        if (isFieldSet(o.props, f) !== (o.stamps[f.number] !== undefined)) {
          throw new Error(`object ${s.id}: field ${f.name} and its stamp disagree`);
        }
      }
      d.objects.set(s.id, o);
    }
    return d;
  }
}

function objectState(o: DocObject): ObjectState {
  const groups: { stamp: Stamp; mask: number }[] = [];
  for (let n = 1; n < FIELD_SLOTS; n++) {
    const st = o.stamps[n];
    if (!st) continue;
    const g = groups.find((x) => compareStamps(x.stamp, st) === 0);
    if (g) g.mask |= 1 << n;
    else groups.push({ stamp: st, mask: 1 << n });
  }
  groups.sort((a, b) => compareStamps(a.stamp, b.stamp));
  return create(ObjectStateSchema, {
    id: o.id,
    props: clone(ObjectPropsSchema, o.props),
    // `>>> 0` keeps bit 31 unsigned, matching uint32.
    stamps: groups.map((g) => create(FieldStampsSchema, { stamp: stampToProto(g.stamp), fieldMask: g.mask >>> 0 })),
  });
}

/** Byte-wise order, the same as Go's sort.Strings for our ASCII ids. */
function compareIds(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}
