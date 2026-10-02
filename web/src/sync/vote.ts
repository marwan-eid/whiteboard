import { create } from "@bufbuild/protobuf";
import { OpSchema, ShapeType, type Op } from "../gen/whiteboard/v1/protocol_pb";
import { ballotId, MAX_VOTES_PER_USER, VOTE_ID } from "./boardWide";
import { isShape, type Doc } from "./doc";

/**
 * Dot voting (docs/ARCHITECTURE.md, "Voting"). The session is the board-wide
 * "_vote" object: its text is the session id, votes_per_user the dots each
 * client gets, closed whether results are shown. Each client writes only its
 * own ballot, "_ballot:<client id>": the session id and the voted-for object
 * ids (one entry per dot). Ballots from earlier sessions are ignored.
 */
export type VoteState =
  | { kind: "none" }
  | { kind: "open"; session: string; votesPerUser: number }
  | { kind: "closed"; session: string };

export function voteState(doc: Doc): VoteState {
  const p = doc.get(VOTE_ID)?.props;
  if (!p?.text) return { kind: "none" };
  return p.closed ? { kind: "closed", session: p.text } : { kind: "open", session: p.text, votesPerUser: p.votesPerUser ?? 1 };
}

function ballotVotes(doc: Doc, id: string, session: string): string[] {
  const p = doc.get(id)?.props;
  return p?.text === session && p.votes ? p.votes.split(",") : [];
}

/** This client's dots in the session, one entry per dot. */
export function myVotes(doc: Doc, clientId: number, session: string): string[] {
  return ballotVotes(doc, ballotId(clientId), session);
}

/** Dots per shape over every ballot in the session; deleted shapes are left out. */
export function tally(doc: Doc, session: string): Map<string, number> {
  const counts = new Map<string, number>();
  for (const o of doc.values()) {
    if (o.props.type !== ShapeType.BALLOT) continue;
    for (const id of ballotVotes(doc, o.id, session)) counts.set(id, (counts.get(id) ?? 0) + 1);
  }
  for (const id of counts.keys()) {
    const o = doc.get(id);
    if (!o || !isShape(o)) counts.delete(id);
  }
  return counts;
}

/** Counts per id in a list of votes. */
export function countVotes(votes: readonly string[]): Map<string, number> {
  const m = new Map<string, number>();
  for (const id of votes) m.set(id, (m.get(id) ?? 0) + 1);
  return m;
}

export function newSessionId(): string {
  const b = crypto.getRandomValues(new Uint8Array(9));
  return btoa(String.fromCharCode(...b)).replace(/\+/g, "-").replace(/\//g, "_");
}

export const voteOps = {
  start(doc: Doc, votesPerUser: number, session = newSessionId()): Op {
    const n = Math.max(1, Math.min(MAX_VOTES_PER_USER, Math.round(votesPerUser)));
    return create(OpSchema, {
      id: VOTE_ID,
      props: { type: doc.get(VOTE_ID) ? undefined : ShapeType.VOTE, text: session, votesPerUser: n, closed: false },
    });
  },
  close(doc: Doc): Op | null {
    return voteState(doc).kind === "open" ? create(OpSchema, { id: VOTE_ID, props: { closed: true } }) : null;
  },
  /** Adds one dot to each id, while dots last; null if nothing changes. */
  add(doc: Doc, clientId: number, ids: readonly string[]): Op | null {
    const s = voteState(doc);
    if (s.kind !== "open") return null;
    const votes = myVotes(doc, clientId, s.session);
    const before = votes.length;
    for (const id of ids) if (votes.length < s.votesPerUser) votes.push(id);
    return votes.length === before ? null : ballot(doc, clientId, s.session, votes);
  },
  /** Takes back this client's dots from the ids; null if nothing changes. */
  remove(doc: Doc, clientId: number, ids: readonly string[]): Op | null {
    const s = voteState(doc);
    if (s.kind !== "open") return null;
    const drop = new Set(ids);
    const votes = myVotes(doc, clientId, s.session);
    const kept = votes.filter((id) => !drop.has(id));
    return kept.length === votes.length ? null : ballot(doc, clientId, s.session, kept);
  },
};

function ballot(doc: Doc, clientId: number, session: string, votes: string[]): Op {
  const id = ballotId(clientId);
  return create(OpSchema, { id, props: { type: doc.get(id) ? undefined : ShapeType.BALLOT, text: session, votes: votes.join(",") } });
}
