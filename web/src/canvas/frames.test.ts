import { describe, expect, it } from "vitest";
import { FrameScheduler } from "./frames";

describe("FrameScheduler", () => {
  it("coalesces requests into one render per frame, after pre-render steps", () => {
    const frames: (() => void)[] = [];
    const log: string[] = [];
    const s = new FrameScheduler(
      () => log.push("render"),
      (cb) => frames.push(cb),
    );
    s.beforeRender(() => log.push("overlay"));
    s.request();
    s.request();
    s.request();
    expect(frames).toHaveLength(1);
    frames.shift()!();
    expect(log).toEqual(["overlay", "render"]);

    s.request();
    expect(frames).toHaveLength(1);
  });
});
