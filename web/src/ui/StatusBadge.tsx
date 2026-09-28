import { useEffect, useState } from "preact/hooks";
import type { Connection, ConnectionState } from "../net/connection";
import type { Presence } from "../sync/presence";
import type { SyncSession } from "../sync/session";

export function StatusBadge({ connection, session, presence, boardId, viewOnly }: { connection: Connection; session: SyncSession; presence: Presence; boardId: string; viewOnly: boolean }) {
  const [state, setState] = useState<ConnectionState>(connection.state);
  const [pending, setPending] = useState(session.pendingCount);
  const [others, setOthers] = useState(presence.others.size);
  useEffect(() => connection.subscribe(setState), [connection]);
  useEffect(() => session.subscribe(() => setPending(session.pendingCount)), [session]);
  useEffect(() => presence.subscribe(() => setOthers(presence.others.size)), [presence]);

  return (
    <div class="status" data-status={state.status}>
      <span class="dot" />
      <span>{describe(state)}</span>
      {pending > 0 && <span class="muted">{pending} unsynced</span>}
      {others > 0 && <span class="muted">{others} other{others === 1 ? "" : "s"} here</span>}
      {viewOnly && <span class="muted" data-testid="view-only">view only</span>}
      <span class="muted">board: {boardId}</span>
    </div>
  );
}

function describe(state: ConnectionState): string {
  switch (state.status) {
    case "connecting":
      return state.attempt === 0 ? "Connecting…" : `Connecting (attempt ${state.attempt + 1})…`;
    case "connected":
      return state.rttMs === null
        ? `Connected to ${state.nodeId}`
        : `Connected to ${state.nodeId} · ${Math.round(state.rttMs)} ms`;
    case "reconnecting":
      return `Reconnecting in ${(state.retryInMs / 1000).toFixed(1)} s`;
    case "rejected":
      return `Disconnected: ${state.reason}`;
  }
}
