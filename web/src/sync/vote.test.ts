import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { OpSchema, ShapeType, type Op } from "../gen/whiteboard/v1/protocol_pb";
import { Doc } from "./doc";
import { myVotes, tally, voteOps, voteState } from "./vote";

describe("voting", () => {
  it("runs a session: dots per client, take-backs, closing, and results", () => {
    const doc = new Doc();
    let wall = 0;
    const apply = (op: Op | null, clientId = 1) => {
      expect(op).not.toBeNull();
      doc.apply(op!, { wallMs: ++wall, counter: 0, clientId });
    };
    for (const id of ["1:a", "1:b", "1:c"]) apply(create(OpSchema, { id, props: { type: ShapeType.STICKY } }));

    expect(voteState(doc)).toEqual({ kind: "none" });
    apply(voteOps.start(doc, 2, "s1"));
    expect(voteState(doc)).toEqual({ kind: "open", session: "s1", votesPerUser: 2 });

    // Client 1 spends both dots; a third is refused.
    apply(voteOps.add(doc, 1, ["1:a", "1:b"]));
    expect(voteOps.add(doc, 1, ["1:c"])).toBeNull();
    // Client 2 puts both on one note, then takes them back and votes once.
    apply(voteOps.add(doc, 2, ["1:a"]), 2);
    apply(voteOps.add(doc, 2, ["1:a"]), 2);
    expect(myVotes(doc, 2, "s1")).toEqual(["1:a", "1:a"]);
    apply(voteOps.remove(doc, 2, ["1:a"]), 2);
    apply(voteOps.add(doc, 2, ["1:c"]), 2);
    expect(Object.fromEntries(tally(doc, "s1"))).toEqual({ "1:a": 1, "1:b": 1, "1:c": 1 });

    // A deleted note drops out of the results.
    apply(create(OpSchema, { id: "1:b", props: { deleted: true } }));
    apply(voteOps.close(doc));
    expect(voteState(doc).kind).toBe("closed");
    expect(Object.fromEntries(tally(doc, "s1"))).toEqual({ "1:a": 1, "1:c": 1 });
    expect(voteOps.add(doc, 1, ["1:c"])).toBeNull();

    // A new session starts from zero: old ballots do not count.
    apply(voteOps.start(doc, 3, "s2"));
    expect(tally(doc, "s2").size).toBe(0);
    expect(myVotes(doc, 1, "s2")).toEqual([]);
  });
});
