import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { OpSchema, ShapeType, WelcomeSchema, type ObjectProps } from "../gen/whiteboard/v1/protocol_pb";
import { History } from "./history";
import { SyncSession } from "./session";

function setup() {
  let t = 1000;
  const session = new SyncSession({ clientId: 1, now: () => t++, send: () => true, resync: () => {} });
  session.onWelcome(create(WelcomeSchema, {}));
  return { session, history: new History(session) };
}

const set = (id: string, props: Partial<ObjectProps>) => create(OpSchema, { id, props });

describe("History", () => {
  it("undoes and redoes a create", () => {
    const { session, history } = setup();
    history.apply([set("1:a", { type: ShapeType.RECT, x: 5, w: 10 })]);
    expect(history.undo()).toBe(true);
    expect(session.doc.get("1:a")?.props.deleted).toBe(true);
    expect(history.redo()).toBe(true);
    expect(session.doc.get("1:a")?.props).toMatchObject({ deleted: false, x: 5, w: 10, type: ShapeType.RECT });
  });

  it("restores previous values, including ones changed by others since", () => {
    const { session, history } = setup();
    session.edit([set("1:a", { type: ShapeType.RECT, x: 0, fill: 1 })]);
    history.apply([set("1:a", { x: 50 })]);
    session.edit([set("1:a", { x: 70 })]); // e.g. a remote edit applied later
    history.undo();
    expect(session.doc.get("1:a")?.props.x).toBe(0);
    expect(session.doc.get("1:a")?.props.fill).toBe(1);
  });

  it("treats a drag as one step whose undo restores the start", () => {
    const { session, history } = setup();
    session.edit([set("1:a", { type: ShapeType.RECT, x: 0, y: 0 })]);
    const g = history.begin();
    for (let i = 1; i <= 10; i++) g.edit([set("1:a", { x: i * 10, y: i })]);
    g.end();
    expect(session.doc.get("1:a")?.props).toMatchObject({ x: 100, y: 10 });
    history.undo();
    expect(session.doc.get("1:a")?.props).toMatchObject({ x: 0, y: 0 });
    history.redo();
    expect(session.doc.get("1:a")?.props).toMatchObject({ x: 100, y: 10 });
    expect(history.canRedo).toBe(false);
  });

  it("uses empty values for fields that had none", () => {
    const { session, history } = setup();
    session.edit([set("1:a", { type: ShapeType.STICKY })]);
    history.apply([set("1:a", { text: "hi", deleted: true })]);
    history.undo();
    expect(session.doc.get("1:a")?.props).toMatchObject({ text: "", deleted: false });
  });

  it("clears redo after a new action and caps the stack", () => {
    const { session, history } = setup();
    const h = new History(session, 3);
    session.edit([set("1:a", { type: ShapeType.RECT, x: 0 })]);
    for (let i = 1; i <= 5; i++) h.apply([set("1:a", { x: i })]);
    let undos = 0;
    while (h.undo()) undos++;
    expect(undos).toBe(3);
    expect(session.doc.get("1:a")?.props.x).toBe(2);
    h.apply([set("1:a", { x: 9 })]);
    expect(h.canRedo).toBe(false);
    expect(history.canUndo).toBe(false);
  });

  it("cancel reverts a gesture without recording it", () => {
    const { session, history } = setup();
    session.edit([set("1:a", { type: ShapeType.RECT, x: 3 })]);
    const g = history.begin();
    g.edit([set("1:a", { x: 40 })]);
    g.edit([set("1:b", { type: ShapeType.ELLIPSE })]);
    g.cancel();
    expect(session.doc.get("1:a")?.props.x).toBe(3);
    expect(session.doc.get("1:b")?.props.deleted).toBe(true);
    expect(history.canUndo).toBe(false);
  });
});
