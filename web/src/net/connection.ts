import type { Frame, OpBatch, Welcome } from "../gen/whiteboard/v1/protocol_pb";
import { ErrorCode } from "../gen/whiteboard/v1/protocol_pb";
import { backoffDelay } from "./backoff";
import { ClockSync } from "./clock";
import { decodeServerMessage, encodeCursor, encodeHello, encodeOpBatch, encodeTimePing } from "./protocol";

export type ConnectionState =
  | { status: "connecting"; attempt: number }
  | { status: "connected"; nodeId: string; rttMs: number | null }
  | { status: "reconnecting"; attempt: number; retryInMs: number }
  /** The server refused us for a reason retrying will not fix. */
  | { status: "rejected"; reason: string };

/** The subset of WebSocket used here, so tests can substitute a fake. */
export interface SocketLike {
  binaryType: BinaryType;
  onopen: ((ev: Event) => void) | null;
  onmessage: ((ev: MessageEvent) => void) | null;
  onclose: ((ev: CloseEvent) => void) | null;
  send(data: Uint8Array<ArrayBuffer>): void;
  close(code?: number, reason?: string): void;
}

/** Board traffic, delivered to the sync session. */
export interface ConnectionHandlers {
  onWelcome(w: Welcome): void;
  onFrame(f: Frame): void;
  onDisconnect(): void;
}

export interface ConnectionOptions {
  url: string;
  boardId: string;
  clientId: number;
  handlers?: Partial<ConnectionHandlers>;
  pingIntervalMs?: number;
  createSocket?: (url: string) => SocketLike;
  /** Monotonic clock in ms, used for RTT. */
  now?: () => number;
  /** Wall clock in ms, used for the server clock offset. */
  wallNow?: () => number;
  random?: () => number;
}

type Listener = (state: ConnectionState) => void;

// Errors that retrying cannot fix: bugs, version skew, or another tab
// taking over our client id.
const FATAL_ERRORS = new Set([ErrorCode.UNSUPPORTED_VERSION, ErrorCode.BAD_REQUEST, ErrorCode.CLIENT_ID_IN_USE]);

/**
 * One logical connection to a board: handshakes, estimates RTT and server
 * clock offset, and reconnects with jittered backoff until stopped or rejected.
 */
export class Connection {
  private readonly opts: Required<Omit<ConnectionOptions, "handlers">> & { handlers: Partial<ConnectionHandlers> };
  private readonly listeners = new Set<Listener>();
  private readonly clock = new ClockSync();
  private socket: SocketLike | null = null;
  private welcomed = false;
  private attempt = 0;
  private stopped = true;
  private retryTimer: ReturnType<typeof setTimeout> | undefined;
  private pingTimer: ReturnType<typeof setInterval> | undefined;
  private _state: ConnectionState = { status: "connecting", attempt: 0 };

  constructor(opts: ConnectionOptions) {
    this.opts = {
      pingIntervalMs: 2_000,
      createSocket: (url) => new WebSocket(url),
      now: () => performance.now(),
      wallNow: () => Date.now(),
      random: Math.random,
      handlers: {},
      ...opts,
    };
  }

  get state(): ConnectionState {
    return this._state;
  }

  /** Current server time estimate, in ms since the epoch. */
  serverNow(): number {
    return this.opts.wallNow() + this.clock.offsetMs;
  }

  /** Calls fn now and on every state change; returns an unsubscribe function. */
  subscribe(fn: Listener): () => void {
    this.listeners.add(fn);
    fn(this._state);
    return () => this.listeners.delete(fn);
  }

  start(): void {
    if (!this.stopped) return;
    this.stopped = false;
    this.attempt = 0;
    this.open();
  }

  stop(): void {
    this.stopped = true;
    clearTimeout(this.retryTimer);
    this.teardownSocket();
  }

  /** Sends a batch if the board has welcomed us; false means not sent. */
  sendBatch(batch: OpBatch): boolean {
    if (!this.socket || !this.welcomed) return false;
    this.socket.send(encodeOpBatch(batch));
    return true;
  }

  /** Sends our pointer position if welcomed; false means not sent. */
  sendCursor(x: number, y: number): boolean {
    if (!this.socket || !this.welcomed) return false;
    this.socket.send(encodeCursor(x, y));
    return true;
  }

  /** Drops the connection and reconnects at once to get a fresh snapshot. */
  resync(): void {
    if (this.stopped) return;
    clearTimeout(this.retryTimer);
    this.teardownSocket();
    this.attempt = 0;
    this.open();
  }

  private open(): void {
    this.setState({ status: "connecting", attempt: this.attempt });
    const socket = this.opts.createSocket(this.opts.url);
    socket.binaryType = "arraybuffer";
    this.socket = socket;
    this.welcomed = false;
    let rejectedReason: string | null = null;

    socket.onopen = () => socket.send(encodeHello(this.opts.boardId, this.opts.clientId));

    socket.onmessage = (ev) => {
      if (!(ev.data instanceof ArrayBuffer)) return;
      const msg = decodeServerMessage(ev.data).msg;
      switch (msg.case) {
        case "welcome":
          this.attempt = 0;
          this.welcomed = true;
          this.clock.seedFrom(Number(msg.value.serverTimeMs), this.opts.wallNow());
          this.setState({ status: "connected", nodeId: msg.value.nodeId, rttMs: null });
          this.opts.handlers.onWelcome?.(msg.value);
          this.startPinging(socket);
          break;
        case "frame":
          this.opts.handlers.onFrame?.(msg.value);
          break;
        case "timePong": {
          const rttMs = this.opts.now() - msg.value.t0;
          this.clock.addSample(rttMs, Number(msg.value.serverTimeMs), this.opts.wallNow());
          if (this._state.status === "connected") this.setState({ ...this._state, rttMs });
          break;
        }
        case "error":
          if (FATAL_ERRORS.has(msg.value.code)) rejectedReason = msg.value.message;
          break;
      }
    };

    socket.onclose = () => {
      if (this.socket !== socket) return;
      this.teardownSocket();
      if (this.stopped) return;
      if (rejectedReason !== null) {
        this.stopped = true;
        this.setState({ status: "rejected", reason: rejectedReason });
        return;
      }
      const retryInMs = backoffDelay(this.attempt, this.opts.random);
      this.attempt++;
      this.setState({ status: "reconnecting", attempt: this.attempt, retryInMs });
      this.retryTimer = setTimeout(() => this.open(), retryInMs);
    };
  }

  private startPinging(socket: SocketLike): void {
    clearInterval(this.pingTimer);
    const ping = () => socket.send(encodeTimePing(this.opts.now()));
    ping();
    this.pingTimer = setInterval(ping, this.opts.pingIntervalMs);
  }

  private teardownSocket(): void {
    clearInterval(this.pingTimer);
    const socket = this.socket;
    const wasWelcomed = this.welcomed;
    this.socket = null;
    this.welcomed = false;
    if (socket) {
      socket.onopen = socket.onmessage = socket.onclose = null;
      socket.close();
      if (wasWelcomed) this.opts.handlers.onDisconnect?.();
    }
  }

  private setState(state: ConnectionState): void {
    this._state = state;
    for (const fn of this.listeners) fn(state);
  }
}
