import { useEffect, useState } from "preact/hooks";
import { shareUrl } from "../board";
import { Role } from "../gen/whiteboard/v1/protocol_pb";
import type { Api, BoardInfo, LinkInfo } from "../net/api";

/** The boards panel: new private board, my boards, and (for owners) share links. */
export function BoardMenu({ api, boardId, role }: { api: Api; boardId: string; role: Role }) {
  const [open, setOpen] = useState(false);
  const isOwner = role === Role.OWNER;
  return (
    <div class="board-menu">
      <button class="menu-button" aria-expanded={open} onClick={() => setOpen(!open)}>
        {isOwner ? "Share" : "Boards"}
      </button>
      {open && (
        <div class="panel" role="dialog" aria-label="Boards">
          {isOwner && <SharePanel api={api} boardId={boardId} />}
          <MyBoards api={api} current={boardId} />
        </div>
      )}
    </div>
  );
}

function MyBoards({ api, current }: { api: Api; current: string }) {
  const [boards, setBoards] = useState<BoardInfo[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api.listBoards().then(setBoards, (e: Error) => setError(e.message));
  }, [api]);

  const create = async () => {
    try {
      const b = await api.createBoard("");
      location.assign(`/b/${b.id}`);
    } catch (e) {
      setError((e as Error).message);
    }
  };

  return (
    <section>
      <h3>My boards</h3>
      <button class="primary" onClick={() => void create()}>
        New private board
      </button>
      {error && <p class="error">{error}</p>}
      {boards?.length === 0 && <p class="muted">Boards you create appear here.</p>}
      <ul>
        {boards?.map((b) => (
          <li key={b.id}>
            <a href={`/b/${b.id}`} aria-current={b.id === current ? "page" : undefined}>
              {b.title || b.id}
            </a>
            <span class="muted">{new Date(b.createdAt).toLocaleDateString()}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function SharePanel({ api, boardId }: { api: Api; boardId: string }) {
  const [links, setLinks] = useState<LinkInfo[]>([]);
  const [fresh, setFresh] = useState<{ role: string; url: string } | null>(null);
  const [error, setError] = useState("");
  const refresh = () => api.listLinks(boardId).then(setLinks, (e: Error) => setError(e.message));
  useEffect(() => void refresh(), [api, boardId]);

  const create = async (role: "viewer" | "editor") => {
    try {
      const l = await api.createLink(boardId, role);
      const url = shareUrl(location.origin, boardId, l.token!);
      setFresh({ role, url });
      await navigator.clipboard?.writeText(url).catch(() => undefined);
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    }
  };
  const revoke = async (id: string) => {
    try {
      await api.revokeLink(boardId, id);
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const active = links.filter((l) => !l.revokedAt);
  return (
    <section>
      <h3>Share this board</h3>
      <p class="muted">Only you and people with a link can open it.</p>
      <div class="row">
        <button onClick={() => void create("viewer")}>Create view link</button>
        <button onClick={() => void create("editor")}>Create edit link</button>
      </div>
      {fresh && (
        <label class="fresh">
          New {fresh.role} link (copied; shown once):
          <input readOnly value={fresh.url} data-testid="share-url" onFocus={(e) => e.currentTarget.select()} />
        </label>
      )}
      {error && <p class="error">{error}</p>}
      {active.length > 0 && (
        <ul>
          {active.map((l) => (
            <li key={l.id}>
              <span>{l.role === "viewer" ? "View" : "Edit"} link</span>
              <span class="muted">{new Date(l.createdAt).toLocaleString()}</span>
              <button onClick={() => void revoke(l.id)}>Revoke</button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
