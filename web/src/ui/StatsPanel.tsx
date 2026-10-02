import { useEffect, useState } from "preact/hooks";
import type { SyncSession } from "../sync/session";

/** What /api/stats/stream sends (internal/httpapi/stats.go). */
interface Stats {
  node: string;
  connections: number;
  boards: number;
  editsPerSec: number;
  syncP50Ms: number;
  syncP99Ms: number;
  samples: number;
  windowS: number;
}

const ms = (v: number) => (v < 10 ? v.toFixed(1) : Math.round(v).toString());

/**
 * Live numbers from the server this browser is connected to, once a second,
 * each labeled with what it measures. Nothing here is a benchmark result.
 */
export function StatsPanel({ session }: { session: SyncSession }) {
  const [open, setOpen] = useState(false);
  const [stats, setStats] = useState<Stats | null>(null);
  const [error, setError] = useState(false);

  useEffect(() => {
    if (!open) return;
    const es = new EventSource("/api/stats/stream");
    es.onmessage = (e: MessageEvent<string>) => {
      setStats(JSON.parse(e.data) as Stats);
      setError(false);
    };
    es.onerror = () => setError(true);
    return () => es.close();
  }, [open]);

  if (!open) {
    return (
      <button class="stats-toggle" title="Live stats" aria-label="Live stats" onClick={() => setOpen(true)}>
        📊
      </button>
    );
  }
  const rtt = session.medianRoundTripMs;
  return (
    <div class="stats" role="dialog" aria-label="Live stats">
      <div class="stats-head">
        <strong>Live stats</strong>
        <button aria-label="Close live stats" onClick={() => setOpen(false)}>
          ✕
        </button>
      </div>
      {!stats ? (
        <p class="muted">{error ? "Stats unavailable." : "Connecting…"}</p>
      ) : (
        <dl data-testid="stats">
          <dt>Open connections</dt>
          <dd data-stat="connections">{stats.connections}</dd>
          <dt>Boards in memory</dt>
          <dd>{stats.boards}</dd>
          <dt>Edits per second</dt>
          <dd data-stat="edits">{stats.editsPerSec.toFixed(1)}</dd>
          <dt>
            Server sync time, p50 / p99
            <small>edit received → committed and sent to others; last {stats.windowS} s, {stats.samples} edits</small>
          </dt>
          <dd>{stats.samples ? `${ms(stats.syncP50Ms)} / ${ms(stats.syncP99Ms)} ms` : "–"}</dd>
          <dt>
            Your round trip, median
            <small>this browser: edit sent → acknowledged; last {session.roundTripSamples} edits</small>
          </dt>
          <dd>{rtt === null ? "–" : `${ms(rtt)} ms`}</dd>
        </dl>
      )}
      {stats && <p class="muted">Server {stats.node}, updated every second.</p>}
    </div>
  );
}
