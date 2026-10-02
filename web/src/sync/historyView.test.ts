import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { HistorySchema } from "../gen/whiteboard/v1/protocol_pb";
import { HistoryView } from "./historyView";

describe("HistoryView", () => {
  let requests: number[];
  let restores: number[];
  let view: HistoryView;

  beforeEach(() => {
    vi.useFakeTimers();
    requests = [];
    restores = [];
    view = new HistoryView({
      request: (s) => (requests.push(s), true),
      restore: (s) => (restores.push(s), true),
      head: () => 40,
      debounceMs: 100,
    });
  });
  afterEach(() => vi.useRealTimers());

  it("debounces scrubbing and ignores stale replies", () => {
    view.open();
    view.seek(30);
    view.seek(12);
    vi.advanceTimersByTime(100);
    expect(requests).toEqual([12]);

    view.onHistory(create(HistorySchema, { seq: 30n }));
    expect(view.doc).toBeNull();
    view.onHistory(create(HistorySchema, { seq: 12n, wallMs: 5n }));
    expect(view.doc?.size).toBe(0);
    expect(view.loading).toBe(false);
    expect(view.wallMs).toBe(5);
  });

  it("clamps to the known versions and restores only past ones", () => {
    view.open();
    view.seek(99);
    expect(view.seq).toBe(40);
    view.restore(); // already the latest: nothing to restore
    expect(restores).toEqual([]);
    expect(view.active).toBe(false);

    view.open();
    view.seek(7);
    view.restore();
    expect(restores).toEqual([7]);
  });
});

describe("HistoryView playback", () => {
  it("steps through history, waiting for each state, and stops at the latest", () => {
    vi.useFakeTimers();
    const requests: number[] = [];
    const view = new HistoryView({ request: (s) => (requests.push(s), true), restore: () => true, head: () => 160, debounceMs: 10 });
    const reply = () => view.onHistory(create(HistorySchema, { seq: BigInt(view.seq), objects: [] }));
    view.open();
    vi.advanceTimersByTime(10);
    reply();
    expect(view.seq).toBe(160);

    view.play(50); // at the latest already, so it starts from the beginning
    expect(view.playing).toBe(true);
    expect(view.seq).toBe(0);
    vi.advanceTimersByTime(10);
    reply();
    vi.advanceTimersByTime(50); // one step: 160 / 80 = 2
    expect(view.seq).toBe(2);
    vi.advanceTimersByTime(200); // still loading 2: no further steps
    expect(view.seq).toBe(2);
    for (let i = 0; i < 100 && view.playing; i++) {
      reply();
      vi.advanceTimersByTime(50);
    }
    expect(view.seq).toBe(160);
    expect(view.playing).toBe(false);
    expect(requests.at(-1)).toBe(160);

    view.play(50);
    view.close();
    expect(view.playing).toBe(false);
    vi.useRealTimers();
  });
});
