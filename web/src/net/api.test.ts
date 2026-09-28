import { describe, expect, it } from "vitest";
import { Api } from "./api";

function memoryStore() {
  const m = new Map<string, string>();
  return {
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => void m.set(k, v),
  };
}

describe("Api", () => {
  it("creates the guest token once and keeps it", async () => {
    const calls: string[] = [];
    const store = memoryStore();
    const fetchFn = (url: string, init?: RequestInit) => {
      calls.push(`${init?.method} ${url} ${new Headers(init?.headers).get("Authorization") ?? ""}`);
      if (url === "/api/guest") return Promise.resolve(Response.json({ token: "g.sig", guestId: "g" }, { status: 201 }));
      return Promise.resolve(Response.json([]));
    };
    const api = new Api(fetchFn, store);
    expect(await api.guestToken()).toBe("g.sig");
    await api.listBoards();
    // A new page load reuses the stored identity.
    await new Api(fetchFn, store).listBoards();
    expect(calls).toEqual(["POST /api/guest ", "GET /api/boards Bearer g.sig", "GET /api/boards Bearer g.sig"]);
  });

  it("opens anonymously when the server is unreachable, and reports API errors", async () => {
    const api = new Api(() => Promise.reject(new Error("offline")), memoryStore());
    expect(await api.guestToken()).toBe("");
    const denied = new Api(
      (url) =>
        Promise.resolve(
          url === "/api/guest"
            ? Response.json({ token: "t" })
            : new Response("only the board owner can do this\n", {
                status: 403,
              }),
        ),
      memoryStore(),
    );
    await expect(denied.listLinks("b")).rejects.toThrow("only the board owner can do this");
  });
});
