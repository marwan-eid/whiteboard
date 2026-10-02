import { useEffect, useState } from "preact/hooks";
import type { Op } from "../gen/whiteboard/v1/protocol_pb";
import { TIMER_ID } from "../sync/boardWide";
import type { SyncSession } from "../sync/session";
import { formatRemaining, remainingAt, timerOps, timerState } from "../sync/timer";

const PRESETS_MIN = [1, 3, 5, 10];

/** The board's shared countdown. Everyone sees it; editors control it. */
export function TimerWidget({ session, now, canEdit }: { session: SyncSession; now: () => number; canEdit: boolean }) {
  const [, setTick] = useState(0);
  const [menu, setMenu] = useState(false);
  const [minutes, setMinutes] = useState(5);
  const rerender = () => setTick((t) => t + 1);
  useEffect(() => session.subscribe((changed) => (!changed || changed.has(TIMER_ID)) && rerender()), [session]);

  const o = session.doc.get(TIMER_ID);
  const state = timerState(o);
  // Redraw a few times a second while running; the time shown is computed from
  // the server clock estimate each time, so it never drifts.
  useEffect(() => {
    if (state.kind !== "running") return;
    const t = setInterval(rerender, 200);
    return () => clearInterval(t);
  }, [state.kind]);

  const act = (op: Op | null) => {
    if (op) session.edit([op]);
    setMenu(false);
  };
  const start = (min: number) => act(timerOps.start(o, min * 60_000, now()));

  if (state.kind === "idle") {
    if (!canEdit) return null;
    return (
      <div class="timer" data-state="idle">
        <button aria-expanded={menu} onClick={() => setMenu(!menu)}>
          ⏱ Timer
        </button>
        {menu && (
          <div class="timer-menu" role="dialog" aria-label="Start a timer">
            {PRESETS_MIN.map((m) => (
              <button key={m} onClick={() => start(m)}>
                {m} min
              </button>
            ))}
            <label>
              <input
                type="number"
                min={1}
                max={1440}
                value={minutes}
                aria-label="Minutes"
                onInput={(e) => setMinutes(Number((e.target as HTMLInputElement).value))}
              />
              min
            </label>
            <button class="primary" disabled={!(minutes >= 1 && minutes <= 1440)} onClick={() => start(minutes)}>
              Start
            </button>
          </div>
        )}
      </div>
    );
  }

  const left = remainingAt(state, now());
  const up = state.kind === "running" && left === 0;
  return (
    <div class="timer" data-state={up ? "up" : state.kind}>
      <span class="time" data-testid="timer" aria-live="off">
        {formatRemaining(left)}
      </span>
      {up && <span class="label">Time's up</span>}
      {state.kind === "paused" && <span class="label">paused</span>}
      {canEdit && (
        <>
          {state.kind === "running" && !up && (
            <button aria-label="Pause timer" title="Pause" onClick={() => act(timerOps.pause(o, now()))}>
              ⏸
            </button>
          )}
          {state.kind === "paused" && (
            <button aria-label="Resume timer" title="Resume" onClick={() => act(timerOps.resume(o, now()))}>
              ▶
            </button>
          )}
          {!up && (
            <button aria-label="Add a minute" title="Add a minute" onClick={() => act(timerOps.addMinute(o, now()))}>
              +1
            </button>
          )}
          <button aria-label="Reset timer" title="Reset" onClick={() => act(timerOps.reset(o))}>
            ✕
          </button>
        </>
      )}
    </div>
  );
}
