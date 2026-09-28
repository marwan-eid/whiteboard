export const DEFAULT_BOARD_ID = "demo";

const BOARD_PATH = /^\/b\/([A-Za-z0-9_-]{1,64})\/?$/;

/** Board id from a /b/{id} path; anything else opens the public demo board. */
export function boardIdFromPath(pathname: string): string {
  return BOARD_PATH.exec(pathname)?.[1] ?? DEFAULT_BOARD_ID;
}

/** Same-origin WebSocket endpoint (Caddy in prod, Vite proxy in dev). */
export function wsUrl(loc: Pick<Location, "protocol" | "host">): string {
  return `${loc.protocol === "https:" ? "wss" : "ws"}://${loc.host}/ws`;
}

/** The share link token from a "#k=<token>" URL fragment. */
export function shareTokenFromHash(hash: string): string {
  return new URLSearchParams(hash.replace(/^#/, "")).get("k") ?? "";
}

/** The URL that opens a board through a share link. */
export function shareUrl(origin: string, boardId: string, token: string): string {
  return `${origin}/b/${boardId}#k=${encodeURIComponent(token)}`;
}
