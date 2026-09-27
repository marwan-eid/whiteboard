import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { CursorUpdateSchema } from "../gen/whiteboard/v1/protocol_pb";
import { Presence, cursorColor, cursorName, type Point } from "./presence";

describe("Presence", () => {
  let sent: Point[];
  let now: number;
  let p: Presence;

  beforeEach(() => {
    vi.useFakeTimers();
    sent = [];
    now = 0;
    p = new Presence((pt) => (sent.push(pt), true), () => now, 50);
  });
  afterEach(() => vi.useRealTimers());

  it("sends the first move at once and throttles the rest, keeping the latest", () => {
    p.move({ x: 1, y: 1 });
    now = 10;
    p.move({ x: 2, y: 2 });
    now = 20;
    p.move({ x: 3, y: 3 });
    expect(sent).toEqual([{ x: 1, y: 1 }]);
    now = 50;
    vi.advanceTimersByTime(40);
    expect(sent).toEqual([{ x: 1, y: 1 }, { x: 3, y: 3 }]);
  });

  it("retries a move that could not be sent", () => {
    let online = false;
    p = new Presence((pt) => online && (sent.push(pt), true), () => now, 50);
    p.move({ x: 1, y: 1 });
    expect(sent).toEqual([]);
    online = true;
    now = 100;
    p.move({ x: 2, y: 2 });
    expect(sent).toEqual([{ x: 2, y: 2 }]);
  });

  it("tracks others and removes those who left", () => {
    let changes = 0;
    p.subscribe(() => changes++);
    p.apply([create(CursorUpdateSchema, { clientId: 7n, x: 1, y: 2 }), create(CursorUpdateSchema, { clientId: 8n, x: 3, y: 4 })]);
    p.apply([create(CursorUpdateSchema, { clientId: 7n, gone: true })]);
    expect([...p.others]).toEqual([[8, { x: 3, y: 4 }]]);
    expect(changes).toBe(2);
    p.reset();
    expect(p.others.size).toBe(0);
  });

  it("gives each client a stable color and name", () => {
    expect(cursorColor(123)).toBe(cursorColor(123));
    expect(cursorName(36 ** 4 + 35)).toBe("Guest 000Z"); // base 36 "1000z", last four
  });
});
