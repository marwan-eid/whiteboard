import { describe, expect, it } from "vitest";
import { DEFAULT_BOARD_ID, boardIdFromPath, wsUrl } from "./board";

describe("boardIdFromPath", () => {
  it.each([
    ["/b/team-retro_1", "team-retro_1"],
    ["/b/abc/", "abc"],
    ["/", DEFAULT_BOARD_ID],
    ["/b/", DEFAULT_BOARD_ID],
    ["/b/has.dot", DEFAULT_BOARD_ID],
    ["/b/a/b", DEFAULT_BOARD_ID],
    [`/b/${"x".repeat(65)}`, DEFAULT_BOARD_ID],
  ])("%s -> %s", (path, want) => {
    expect(boardIdFromPath(path)).toBe(want);
  });
});

describe("wsUrl", () => {
  it("matches the page scheme", () => {
    expect(wsUrl({ protocol: "http:", host: "localhost:8080" })).toBe("ws://localhost:8080/ws");
    expect(wsUrl({ protocol: "https:", host: "board.example" })).toBe("wss://board.example/ws");
  });
});
