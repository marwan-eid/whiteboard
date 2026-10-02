import { create, equals, fromJson, type JsonValue } from "@bufbuild/protobuf";
import fc from "fast-check";
import { describe, expect, it } from "vitest";
import vectors from "../../../testdata/lww-vectors.json";
import {
  ObjectPropsSchema,
  ObjectStateSchema,
  OpSchema,
  SequencedBatchSchema,
  type ObjectProps,
  type Op,
} from "../gen/whiteboard/v1/protocol_pb";
import { Doc } from "./doc";
import { compareStamps, stampFromProto, type Stamp } from "./stamp";

function permutations(n: number): number[][] {
  if (n === 0) return [[]];
  return permutations(n - 1).flatMap((p) =>
    Array.from({ length: p.length + 1 }, (_, i) => [...p.slice(0, i), n - 1, ...p.slice(i)]),
  );
}

describe("shared LWW vectors (testdata/lww-vectors.json)", () => {
  for (const c of vectors.cases) {
    it(c.name, () => {
      const batches = c.batches.map((b) => fromJson(SequencedBatchSchema, b as JsonValue));
      const expected = c.expected.map((e) => fromJson(ObjectStateSchema, e as JsonValue));
      for (const order of permutations(batches.length)) {
        const d = new Doc();
        for (const i of order) d.applyBatch(batches[i]!.ops, stampFromProto(batches[i]!.stamp));
        const got = d.snapshot();
        expect(got.length, `order ${order.join(",")}`).toBe(expected.length);
        got.forEach((s, i) => {
          expect(equals(ObjectStateSchema, s, expected[i]!), `order ${order.join(",")}: ${s.id}`).toBe(true);
        });
      }
    });
  }
});

// --- property-based -------------------------------------------------------

interface GenBatch {
  stamp: Stamp;
  ops: Op[];
}

const propsArb: fc.Arbitrary<ObjectProps> = fc
  .record(
    {
      type: fc.integer({ min: 1, max: 6 }),
      deleted: fc.boolean(),
      x: fc.integer({ min: -5, max: 5 }),
      fill: fc.integer({ min: 0, max: 0xffff_ffff }),
      text: fc.stringMatching(/^[a-c]{0,3}$/),
    },
    { requiredKeys: [] },
  )
  .map((r) => {
    const p = create(ObjectPropsSchema, {});
    if (r.type !== undefined) p.type = r.type;
    if (r.deleted !== undefined) p.deleted = r.deleted;
    if (r.fill !== undefined) p.fill = r.fill;
    if (r.text !== undefined) p.text = r.text;
    if (r.x !== undefined || Object.keys(r).length === 0) p.x = r.x ?? 0;
    return p;
  });

const batchesArb: fc.Arbitrary<GenBatch[]> = fc
  .uniqueArray(
    fc.record({
      stamp: fc.record({
        wallMs: fc.integer({ min: 1, max: 6 }),
        counter: fc.integer({ min: 0, max: 2 }),
        clientId: fc.integer({ min: 1, max: 3 }),
      }),
      ops: fc.array(
        fc.record({ id: fc.constantFrom("1:a", "1:b", "2:a"), props: propsArb }).map((o) => create(OpSchema, o)),
        { minLength: 1, maxLength: 3 },
      ),
    }),
    // A stamp identifies one write; never reuse it for different content.
    { minLength: 1, maxLength: 25, selector: (b) => `${b.stamp.wallMs}/${b.stamp.counter}/${b.stamp.clientId}` },
  );

function applyAll(batches: GenBatch[], order: number[]): Doc {
  const d = new Doc();
  for (const i of order) d.applyBatch(batches[i]!.ops, batches[i]!.stamp);
  return d;
}

function snapshotsEqual(a: Doc, b: Doc): boolean {
  const sa = a.snapshot();
  const sb = b.snapshot();
  return sa.length === sb.length && sa.every((s, i) => equals(ObjectStateSchema, s, sb[i]!));
}

const orderArb = (n: number) => fc.shuffledSubarray([...Array(n).keys()], { minLength: n, maxLength: n });

describe("Doc properties", () => {
  it("converges regardless of delivery order and duplicates", () => {
    fc.assert(
      fc.property(
        batchesArb.chain((bs) =>
          fc.tuple(fc.constant(bs), orderArb(bs.length), orderArb(bs.length), fc.array(fc.nat({ max: bs.length - 1 }))),
        ),
        ([batches, a, b, dups]) => snapshotsEqual(applyAll(batches, a), applyAll(batches, [...b, ...dups])),
      ),
    );
  });

  it("matches a last-writer-wins model", () => {
    fc.assert(
      fc.property(
        batchesArb.chain((bs) => fc.tuple(fc.constant(bs), orderArb(bs.length))),
        ([batches, order]) => {
          const d = applyAll(batches, order);
          const winner = new Map<string, { stamp: Stamp; value: unknown }>();
          for (const b of batches) {
            for (const op of b.ops) {
              for (const [k, v] of Object.entries(op.props!)) {
                if (k === "$typeName" || v === undefined) continue;
                const key = `${op.id}.${k}`;
                const w = winner.get(key);
                // ">=": within one batch (one stamp), the later write wins.
                if (!w || compareStamps(b.stamp, w.stamp) >= 0) winner.set(key, { stamp: b.stamp, value: v });
              }
            }
          }
          for (const [key, w] of winner) {
            const [id, field] = key.split(".") as [string, keyof ObjectProps];
            expect(d.get(id)!.props[field]).toEqual(w.value);
          }
        },
      ),
    );
  });

  it("restores from a snapshot midway and ends identical", () => {
    fc.assert(
      fc.property(
        batchesArb.chain((bs) => fc.tuple(fc.constant(bs), orderArb(bs.length), fc.nat({ max: bs.length }))),
        ([batches, order, cut]) => {
          const full = applyAll(batches, order);
          const restored = Doc.fromSnapshot(applyAll(batches, order.slice(0, cut)).snapshot());
          for (const i of order.slice(cut)) restored.applyBatch(batches[i]!.ops, batches[i]!.stamp);
          return snapshotsEqual(full, restored);
        },
      ),
    );
  });
});

describe("Doc edge cases", () => {
  it("ignores empty ops", () => {
    const d = new Doc();
    expect(d.apply(create(OpSchema, { id: "1:a" }), { wallMs: 1, counter: 0, clientId: 1 })).toBe(false);
    expect(d.size).toBe(0);
  });

  it("rejects snapshots whose stamps disagree with set fields", () => {
    const bad = fromJson(ObjectStateSchema, {
      id: "1:a",
      props: { x: 1 },
      stamps: [{ stamp: { wallMs: 1 }, fieldMask: 16 }],
    });
    expect(() => Doc.fromSnapshot([bad])).toThrow(/disagree/);
  });

  it("encodes a mask with bit 31 as an unsigned value", () => {
    // Guard for `>>> 0`: masks must never go negative on the wire.
    const d = new Doc();
    d.apply(create(OpSchema, { id: "1:a", props: { text: "t" } }), { wallMs: 1, counter: 0, clientId: 1 });
    expect(d.snapshot()[0]!.stamps[0]!.fieldMask).toBe(1 << 11);
  });
});

describe("Doc.mergeState", () => {
  it("rebuilds a replica from full states, whatever it saw before", () => {
    fc.assert(
      fc.property(
        batchesArb.chain((bs) => fc.tuple(fc.constant(bs), orderArb(bs.length), fc.nat({ max: bs.length }))),
        ([batches, order, cut]) => {
          const full = applyAll(batches, order);
          const partial = applyAll(batches, order.slice(0, cut));
          for (const s of full.snapshot()) partial.mergeState(s);
          return snapshotsEqual(full, partial);
        },
      ),
    );
  });
});
