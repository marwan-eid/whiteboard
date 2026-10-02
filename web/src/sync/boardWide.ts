/**
 * Board-wide objects hold board state rather than shapes: the timer, the vote
 * and each client's ballot. Every client holds them whatever its viewport, and
 * they are never drawn on the canvas. Mirrors internal/protocol (Go).
 */

export const TIMER_ID = "_timer";
export const VOTE_ID = "_vote";
export const MAX_VOTES_PER_USER = 20;
export const MAX_TIMER_MS = 24 * 60 * 60 * 1000;

export function isBoardWide(id: string): boolean {
  return id.startsWith("_");
}

/** This client's ballot in the current vote. */
export function ballotId(clientId: number): string {
  return `_ballot:${clientId.toString(36)}`;
}
