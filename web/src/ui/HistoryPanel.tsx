import { useEffect, useState } from "preact/hooks";
import type { HistoryView } from "../sync/historyView";

/** The version slider shown in history mode. */
export function HistoryPanel({ view }: { view: HistoryView }) {
  const [, setTick] = useState(0);
  useEffect(() => view.subscribe(() => setTick((t) => t + 1)), [view]);
  if (!view.active) return null;

  const when = view.wallMs ? new Date(view.wallMs).toLocaleString() : "";
  return (
    <div class="history" role="dialog" aria-label="Version history">
      <div class="history-row">
        <strong>History</strong>
        <span class="muted" data-testid="history-label">
          Version {view.seq} of {view.head}
          {when && ` · ${when}`}
          {view.loading && " · loading…"}
        </span>
      </div>
      <input
        type="range"
        min={0}
        max={view.head}
        value={view.seq}
        aria-label="Version"
        onInput={(e) => view.seek(Number((e.target as HTMLInputElement).value))}
      />
      <div class="history-row">
        <button class="primary" disabled={view.seq >= view.head || view.loading} onClick={() => view.restore()}>
          Restore this version
        </button>
        <button onClick={() => view.close()}>Back to live</button>
      </div>
    </div>
  );
}
