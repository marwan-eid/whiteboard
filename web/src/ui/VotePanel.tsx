import { useEffect, useMemo, useState } from "preact/hooks";
import type { Editor } from "../canvas/editor";
import { ShapeType, type Op } from "../gen/whiteboard/v1/protocol_pb";
import { isBoardWide, MAX_VOTES_PER_USER, VOTE_ID } from "../sync/boardWide";
import type { Doc } from "../sync/doc";
import type { SyncSession } from "../sync/session";
import { countVotes, myVotes, tally, voteOps, voteState } from "../sync/vote";

const NO_BADGES: ReadonlyMap<string, number> = new Map();

/** A short label for a voted-for object: its text, or its kind. */
function label(doc: Doc, id: string): string {
  const p = doc.get(id)?.props;
  const text = p?.text?.trim();
  if (text) return text.length > 40 ? `${text.slice(0, 39)}…` : text;
  const kind = ShapeType[p?.type ?? 0] ?? "shape";
  return kind.charAt(0) + kind.slice(1).toLowerCase();
}

/**
 * Dot voting: an editor starts a session; everyone with edit access places
 * their dots on selected shapes; ending it shows the totals. While it is
 * open each person sees only their own dots on the canvas.
 */
export function VotePanel({
  session,
  editor,
  canEdit,
  onBadges,
}: {
  session: SyncSession;
  editor: Editor;
  canEdit: boolean;
  onBadges: (badges: ReadonlyMap<string, number>) => void;
}) {
  const [version, setVersion] = useState(0);
  const [, setSelectionTick] = useState(0);
  const [form, setForm] = useState(false);
  const [dots, setDots] = useState(3);
  const [hidden, setHidden] = useState<string | null>(null); // session whose results were dismissed
  useEffect(
    () =>
      session.subscribe((changed) => {
        // Results also depend on shapes being deleted, so any change may matter once a vote exists.
        if (!changed || session.doc.get(VOTE_ID) || [...changed].some(isBoardWide)) setVersion((v) => v + 1);
      }),
    [session],
  );
  useEffect(() => editor.subscribe(() => setSelectionTick((t) => t + 1)), [editor]);

  const doc = session.doc;
  const state = voteState(doc);
  const mine = state.kind === "open" ? myVotes(doc, session.clientId, state.session) : [];
  // Recomputed when the board changes (version), not on every render.
  const results = useMemo(() => (state.kind === "closed" ? tally(doc, state.session) : null), [version, state.kind]);

  const badges = state.kind === "open" ? countVotes(mine) : (results ?? NO_BADGES);
  const shown = state.kind === "closed" && hidden === state.session ? NO_BADGES : badges;
  const badgeKey = [...shown].map(([id, n]) => `${id}=${n}`).join(",");
  useEffect(() => onBadges(shown), [badgeKey, onBadges]);

  const act = (op: Op | null) => {
    if (op) session.edit([op]);
  };
  const selected = [...editor.selection];

  if (state.kind === "open") {
    const left = state.votesPerUser - mine.length;
    return (
      <div class="vote" role="region" aria-label="Dot vote">
        <strong>Dot vote</strong>
        <span class="muted" data-testid="dots-left">
          {canEdit ? `${left} of ${state.votesPerUser} dots left` : "In progress (view only)"}
        </span>
        {canEdit && (
          <>
            <span class="muted">Select shapes, then:</span>
            <div class="row">
              <button class="primary" disabled={left === 0 || selected.length === 0} onClick={() => act(voteOps.add(doc, session.clientId, selected))}>
                Vote for selection
              </button>
              <button disabled={!selected.some((id) => mine.includes(id))} onClick={() => act(voteOps.remove(doc, session.clientId, selected))}>
                Take back
              </button>
            </div>
            <button onClick={() => act(voteOps.close(doc))}>End vote and show results</button>
          </>
        )}
      </div>
    );
  }

  if (state.kind === "closed" && hidden !== state.session && results) {
    const ranked = [...results].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0])).slice(0, 10);
    return (
      <div class="vote" role="region" aria-label="Vote results">
        <strong>Results</strong>
        {ranked.length === 0 && <span class="muted">No votes.</span>}
        <ol data-testid="vote-results">
          {ranked.map(([id, n]) => (
            <li key={id}>
              <button class="link" onClick={() => editor.select([id])}>
                {label(doc, id)}
              </button>
              <span class="count">{n}</span>
            </li>
          ))}
        </ol>
        <div class="row">
          {canEdit && (
            <button
              onClick={() => {
                setHidden(state.session);
                setForm(true);
              }}
            >
              New vote
            </button>
          )}
          <button onClick={() => setHidden(state.session)}>Hide</button>
        </div>
      </div>
    );
  }

  if (!canEdit) return null;
  return (
    <div class="vote compact">
      {!form ? (
        <button onClick={() => setForm(true)}>🗳 Vote</button>
      ) : (
        <div class="row" role="dialog" aria-label="Start a dot vote">
          <label>
            Dots each
            <input
              type="number"
              min={1}
              max={MAX_VOTES_PER_USER}
              value={dots}
              onInput={(e) => setDots(Number((e.target as HTMLInputElement).value))}
            />
          </label>
          <button
            class="primary"
            disabled={!(dots >= 1 && dots <= MAX_VOTES_PER_USER)}
            onClick={() => {
              act(voteOps.start(doc, dots));
              setForm(false);
            }}
          >
            Start vote
          </button>
          <button onClick={() => setForm(false)}>Cancel</button>
        </div>
      )}
    </div>
  );
}
