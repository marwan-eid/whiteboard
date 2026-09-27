export interface BackoffOptions {
  baseMs: number;
  capMs: number;
}

export const DEFAULT_BACKOFF: BackoffOptions = { baseMs: 250, capMs: 10_000 };

/**
 * Reconnect delay for the given attempt (0-based): exponential growth capped
 * at capMs, with "equal jitter" (half fixed, half random) so that thousands of
 * clients dropped by the same node failure do not reconnect in lockstep.
 */
export function backoffDelay(
  attempt: number,
  random: () => number = Math.random,
  { baseMs, capMs }: BackoffOptions = DEFAULT_BACKOFF,
): number {
  const ceiling = Math.min(capMs, baseMs * 2 ** Math.max(0, attempt));
  return ceiling / 2 + random() * (ceiling / 2);
}
