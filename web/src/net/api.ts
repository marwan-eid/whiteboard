/** The board and share-link HTTP API (internal/httpapi/boards.go). */

const GUEST_KEY = "whiteboard.guest";

export interface BoardInfo {
  id: string;
  title: string;
  visibility: string;
  createdAt: string;
}

export interface LinkInfo {
  id: string;
  /** Only returned when the link is created. */
  token?: string;
  role: "viewer" | "editor";
  createdAt: string;
  revokedAt?: string;
}

type Fetch = (input: string, init?: RequestInit) => Promise<Response>;

/** Storage that may be missing or throw (private mode, blocked site data). */
interface KeyValue {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

function safeGet(store: KeyValue | undefined, key: string): string | null {
  try {
    return store?.getItem(key) ?? null;
  } catch {
    return null;
  }
}

function safeSet(store: KeyValue | undefined, key: string, value: string): void {
  try {
    store?.setItem(key, value);
  } catch {
    // Without storage the identity lasts for this page only.
  }
}

export class Api {
  private token: string | null = null;

  constructor(
    private readonly fetchFn: Fetch = (i, init) => fetch(i, init),
    private readonly store: KeyValue | undefined = globalThis.localStorage,
  ) {}

  /**
   * The guest token that identifies this browser, created on first use.
   * Resolves to "" if the server cannot be reached; the board still opens
   * anonymously (public boards and share links work without one).
   */
  async guestToken(): Promise<string> {
    if (this.token) return this.token;
    const stored = safeGet(this.store, GUEST_KEY);
    if (stored) return (this.token = stored);
    try {
      const res = await this.fetchFn("/api/guest", { method: "POST", signal: AbortSignal.timeout(5_000) });
      if (!res.ok) return "";
      const body = (await res.json()) as { token: string };
      safeSet(this.store, GUEST_KEY, body.token);
      return (this.token = body.token);
    } catch {
      return "";
    }
  }

  /** The node serving a board (internal/cluster); no guest token needed. */
  async route(boardId: string): Promise<string> {
    const res = await this.fetchFn(`/api/boards/${encodeURIComponent(boardId)}/route`, { signal: AbortSignal.timeout(3_000) });
    if (!res.ok) throw new Error(`route: ${res.status}`);
    return ((await res.json()) as { nodeId: string }).nodeId;
  }

  createBoard(title: string): Promise<BoardInfo> {
    return this.call("POST", "/api/boards", { title });
  }

  listBoards(): Promise<BoardInfo[]> {
    return this.call("GET", "/api/boards");
  }

  createLink(boardId: string, role: "viewer" | "editor"): Promise<LinkInfo> {
    return this.call("POST", `/api/boards/${encodeURIComponent(boardId)}/links`, { role });
  }

  listLinks(boardId: string): Promise<LinkInfo[]> {
    return this.call("GET", `/api/boards/${encodeURIComponent(boardId)}/links`);
  }

  revokeLink(boardId: string, linkId: string): Promise<void> {
    return this.call("DELETE", `/api/boards/${encodeURIComponent(boardId)}/links/${encodeURIComponent(linkId)}`);
  }

  private async call<T>(method: string, path: string, body?: unknown): Promise<T> {
    const token = await this.guestToken();
    const headers: Record<string, string> = {
      Authorization: `Bearer ${token}`,
    };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    const res = await this.fetchFn(path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (!res.ok) throw new Error((await res.text()).trim() || `${res.status}`);
    return (res.status === 204 ? undefined : await res.json()) as T;
  }
}
