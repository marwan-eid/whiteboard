import { create } from "@bufbuild/protobuf";
import { StampSchema, type Stamp as PbStamp } from "../gen/whiteboard/v1/protocol_pb";

/** Hybrid logical clock timestamp; mirrors internal/hlc.Stamp. */
export interface Stamp {
  readonly wallMs: number;
  readonly counter: number;
  readonly clientId: number;
}

export const ZERO_STAMP: Stamp = { wallMs: 0, counter: 0, clientId: 0 };

/** Orders by (wallMs, counter, clientId). */
export function compareStamps(a: Stamp, b: Stamp): number {
  return a.wallMs - b.wallMs || a.counter - b.counter || a.clientId - b.clientId;
}

// The 64-bit wire fields are bigint in generated code; every value we use is
// below 2^53 (see the note at the top of protocol.proto), so conversion is exact.
export function stampFromProto(p: PbStamp | undefined): Stamp {
  return p ? { wallMs: Number(p.wallMs), counter: p.counter, clientId: Number(p.clientId) } : ZERO_STAMP;
}

export function stampToProto(s: Stamp): PbStamp {
  return create(StampSchema, { wallMs: BigInt(s.wallMs), counter: s.counter, clientId: BigInt(s.clientId) });
}

const MAX_COUNTER = 0xffff_ffff;

/** Issues strictly increasing stamps; mirrors internal/hlc.Clock. */
export class HybridClock {
  private wallMs = 0;
  private counter = 0;

  constructor(
    private readonly clientId: number,
    private readonly physicalNow: () => number,
  ) {}

  now(): Stamp {
    const phys = Math.floor(this.physicalNow());
    if (phys > this.wallMs) {
      this.wallMs = phys;
      this.counter = 0;
    } else {
      this.tick();
    }
    return { wallMs: this.wallMs, counter: this.counter, clientId: this.clientId };
  }

  /** Moves the clock past a stamp seen from elsewhere. */
  observe(s: Stamp): void {
    if (s.wallMs > this.wallMs) {
      this.wallMs = s.wallMs;
      this.counter = s.counter;
    } else if (s.wallMs === this.wallMs && s.counter > this.counter) {
      this.counter = s.counter;
    }
  }

  private tick(): void {
    if (this.counter === MAX_COUNTER) {
      this.wallMs++;
      this.counter = 0;
    } else {
      this.counter++;
    }
  }
}
