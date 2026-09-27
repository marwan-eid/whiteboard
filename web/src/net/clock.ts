interface Sample {
  rttMs: number;
  offsetMs: number;
}

/**
 * Estimates (server clock - local clock) NTP-style: each ping gives
 * offset = serverTime + rtt/2 - localTimeAtReceive, and the sample with the
 * lowest RTT among the most recent few is the least distorted by queuing.
 */
export class ClockSync {
  private samples: Sample[] = [];
  private seed: number | null = null;

  constructor(private readonly keep = 8) {}

  /** A rough estimate from the Welcome, used until the first ping returns. */
  seedFrom(serverMs: number, localWallMs: number): void {
    this.seed = serverMs - localWallMs;
  }

  addSample(rttMs: number, serverMs: number, localWallAtReceiveMs: number): void {
    this.samples.push({ rttMs, offsetMs: serverMs + rttMs / 2 - localWallAtReceiveMs });
    if (this.samples.length > this.keep) this.samples.shift();
  }

  /** Best current estimate in ms, or 0 before any information arrives. */
  get offsetMs(): number {
    let best: Sample | undefined;
    for (const s of this.samples) if (!best || s.rttMs < best.rttMs) best = s;
    return best?.offsetMs ?? this.seed ?? 0;
  }

  reset(): void {
    this.samples = [];
    this.seed = null;
  }
}
