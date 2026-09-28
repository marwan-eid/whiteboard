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
