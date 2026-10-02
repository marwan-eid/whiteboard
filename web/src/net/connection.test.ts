import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  ClientMessageSchema,
  FrameSchema,
  OpBatchSchema,
  ErrorCode,
  MovedSchema,
  ServerErrorSchema,
  ServerMessageSchema,
  TimePongSchema,
  WelcomeSchema,
  type ClientMessage,
  type ServerMessage,
} from "../gen/whiteboard/v1/protocol_pb";
import { Connection, type ConnectionState, type SocketLike } from "./connection";
import { PROTOCOL_VERSION } from "./protocol";

class FakeSocket implements SocketLike {
  binaryType: BinaryType = "blob";
  onopen: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onclose: ((ev: CloseEvent) => void) | null = null;
  sent: ClientMessage[] = [];
  closed = false;
  url = "";

  send(data: Uint8Array<ArrayBuffer>): void {
    this.sent.push(fromBinary(ClientMessageSchema, data));
  }
  close(): void {
    this.closed = true;
  }

  // Test controls.
  serverOpen(): void {
    this.onopen?.(new Event("open"));
  }
  serverSend(msg: ServerMessage["msg"]): void {
    const bytes = toBinary(ServerMessageSchema, create(ServerMessageSchema, { msg }));
    const buf = bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);
    this.onmessage?.({ data: buf } as MessageEvent);
  }
  serverClose(): void {
    this.onclose?.({ code: 1006 } as CloseEvent);
  }
}

function welcome(nodeId: string): ServerMessage["msg"] {
  return { case: "welcome", value: create(WelcomeSchema, { nodeId, protocolVersion: PROTOCOL_VERSION }) };
}

describe("Connection", () => {
  let sockets: FakeSocket[];
  let clock: number;
  let states: ConnectionState[];
  let conn: Connection;
  let wall: number;
  let handlers: { welcomes: number; frames: number; disconnects: number; onWelcome(): void; onFrame(): void; onDisconnect(): void };

  beforeEach(() => {
    vi.useFakeTimers();
    sockets = [];
    clock = 1_000;
    wall = 1_700_000_000_000;
    handlers = {
      welcomes: 0,
      frames: 0,
      disconnects: 0,
      onWelcome() {
        this.welcomes++;
      },
      onFrame() {
        this.frames++;
      },
      onDisconnect() {
        this.disconnects++;
      },
    };
    states = [];
    conn = new Connection({
      url: "ws://test/ws",
      boardId: "demo",
      clientId: 7,
      handlers,
      pingIntervalMs: 2_000,
      wallNow: () => wall,
      createSocket: (url) => {
        const s = new FakeSocket();
        s.url = url;
        sockets.push(s);
        return s;
      },
      now: () => clock,
      random: () => 1,
      credentials: () => ({ guestToken: "g.sig", shareToken: "share" }),
    });
    conn.subscribe((s) => states.push(s));
  });

  afterEach(() => {
    conn.stop();
    vi.useRealTimers();
  });

  const latest = () => sockets[sockets.length - 1]!;

  it("sends hello on open and becomes connected on welcome", () => {
    conn.start();
    latest().serverOpen();
    const hello = latest().sent[0]?.msg;
    expect(hello?.case).toBe("hello");
    expect(hello?.value).toMatchObject({ boardId: "demo", clientId: 7n, protocolVersion: PROTOCOL_VERSION, guestToken: "g.sig", shareToken: "share" });

    latest().serverSend(welcome("node-1"));
    expect(conn.state).toEqual({ status: "connected", nodeId: "node-1", rttMs: null });
    expect(latest().binaryType).toBe("arraybuffer");
  });

  it("measures RTT from pings echoed by the server", () => {
    conn.start();
    latest().serverOpen();
    latest().serverSend(welcome("node-1"));

    const ping = latest().sent[1]?.msg;
    expect(ping?.case).toBe("timePing");
    const t0 = ping?.case === "timePing" ? ping.value.t0 : NaN;
    clock += 42;
    latest().serverSend({ case: "timePong", value: create(TimePongSchema, { t0 }) });
    expect(conn.state).toMatchObject({ status: "connected", rttMs: 42 });

    vi.advanceTimersByTime(2_000);
    expect(latest().sent.filter((m) => m.msg.case === "timePing")).toHaveLength(2);
  });

  it("reconnects with growing backoff and resets after welcome", () => {
    conn.start();
    latest().serverClose();
    expect(conn.state).toEqual({ status: "reconnecting", attempt: 1, retryInMs: 250 });

    vi.advanceTimersByTime(249);
    expect(sockets).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(sockets).toHaveLength(2);

    latest().serverClose();
    expect(conn.state).toEqual({ status: "reconnecting", attempt: 2, retryInMs: 500 });
    vi.advanceTimersByTime(500);
    latest().serverOpen();
    latest().serverSend(welcome("node-2"));
    expect(conn.state).toMatchObject({ status: "connected", nodeId: "node-2" });

    latest().serverClose();
    expect(conn.state).toEqual({ status: "reconnecting", attempt: 1, retryInMs: 250 });
  });

  it("stops pinging a dead socket", () => {
    conn.start();
    latest().serverOpen();
    latest().serverSend(welcome("node-1"));
    const dead = latest();
    dead.serverClose();
    const sentBefore = dead.sent.length;
    vi.advanceTimersByTime(10_000);
    expect(dead.sent).toHaveLength(sentBefore);
  });

  it("does not retry after a protocol rejection", () => {
    conn.start();
    latest().serverOpen();
    latest().serverSend({
      case: "error",
      value: create(ServerErrorSchema, {
        code: ErrorCode.UNSUPPORTED_VERSION,
        message: "server speaks protocol version 2",
      }),
    });
    latest().serverClose();
    expect(conn.state).toEqual({ status: "rejected", reason: "server speaks protocol version 2" });
    vi.advanceTimersByTime(60_000);
    expect(sockets).toHaveLength(1);
  });

  it("sends batches only once welcomed and routes board traffic to handlers", () => {
    conn.start();
    latest().serverOpen();
    const batch = create(OpBatchSchema, { clientSeq: 1n });
    expect(conn.sendBatch(batch)).toBe(false);

    latest().serverSend(welcome("node-1"));
    expect(handlers.welcomes).toBe(1);
    expect(conn.sendBatch(batch)).toBe(true);
    expect(latest().sent.at(-1)?.msg.case).toBe("opBatch");

    latest().serverSend({ case: "frame", value: create(FrameSchema, {}) });
    expect(handlers.frames).toBe(1);

    latest().serverClose();
    expect(handlers.disconnects).toBe(1);
    expect(conn.sendBatch(batch)).toBe(false);
  });

  it("resync() reconnects at once without backoff", () => {
    conn.start();
    latest().serverOpen();
    latest().serverSend(welcome("node-1"));
    conn.resync();
    expect(sockets).toHaveLength(2);
    expect(sockets[0]!.closed).toBe(true);
    expect(conn.state).toEqual({ status: "connecting", attempt: 0 });
  });

  it("estimates the server clock offset", () => {
    conn.start();
    latest().serverOpen();
    latest().serverSend({
      case: "welcome",
      value: create(WelcomeSchema, { nodeId: "n", serverTimeMs: BigInt(wall + 5_000) }),
    });
    expect(conn.serverNow()).toBe(wall + 5_000);

    // A ping with 40 ms RTT, answered when the server clock read wall+3000.
    const ping = latest().sent.find((m) => m.msg.case === "timePing")!.msg;
    const t0 = ping.case === "timePing" ? ping.value.t0 : NaN;
    clock += 40;
    latest().serverSend({
      case: "timePong",
      value: create(TimePongSchema, { t0, serverTimeMs: BigInt(wall + 3_000) }),
    });
    expect(conn.serverNow()).toBe(wall + 3_020);
  });

  it("stops when another tab takes over the client id", () => {
    conn.start();
    latest().serverOpen();
    latest().serverSend(welcome("node-1"));
    latest().serverSend({
      case: "error",
      value: create(ServerErrorSchema, { code: ErrorCode.CLIENT_ID_IN_USE, message: "replaced" }),
    });
    latest().serverClose();
    expect(conn.state).toEqual({ status: "rejected", reason: "replaced" });
    vi.advanceTimersByTime(60_000);
    expect(sockets).toHaveLength(1);
  });

  it("stops when access is denied or revoked", () => {
    conn.start();
    latest().serverOpen();
    latest().serverSend({ case: "error", value: create(ServerErrorSchema, { code: ErrorCode.FORBIDDEN, message: "access revoked" }) });
    latest().serverClose();
    expect(conn.state).toEqual({ status: "rejected", reason: "access revoked" });
    vi.advanceTimersByTime(60_000);
    expect(sockets).toHaveLength(1);
  });

  it("goes straight to the node named by Moved, and back to the default after a failure", () => {
    conn.start();
    expect(latest().url).toBe("ws://test/ws");
    latest().serverOpen();
    latest().serverSend({ case: "moved", value: create(MovedSchema, { nodeId: "node-2" }) });
    latest().serverClose();
    expect(sockets).toHaveLength(2);
    expect(latest().url).toBe("ws://test/n/node-2/ws");
    // That node dies: the next attempt, after backoff, starts from the default again.
    latest().serverClose();
    expect(conn.state.status).toBe("reconnecting");
    vi.advanceTimersByTime(60_000);
    expect(latest().url).toBe("ws://test/ws");
  });

  it("asks the route before connecting, falling back to the url if it fails", async () => {
    const routes = [() => Promise.resolve("ws://test/n/node-3/ws"), () => Promise.reject(new Error("down"))];
    const routed = new Connection({
      url: "ws://test/ws",
      boardId: "demo",
      clientId: 8,
      route: () => routes.shift()!(),
      createSocket: (url) => {
        const s = new FakeSocket();
        s.url = url;
        sockets.push(s);
        return s;
      },
    });
    routed.start();
    await vi.waitFor(() => expect(latest().url).toBe("ws://test/n/node-3/ws"));
    routed.resync();
    await vi.waitFor(() => expect(latest().url).toBe("ws://test/ws"));
    routed.stop();
  });

  it("leaves a server that goes silent, after the welcome or during the handshake", () => {
    conn.start();
    latest().serverOpen();
    latest().serverSend(welcome("node-1"));
    // Pings go out, but nothing comes back: a frozen server.
    for (let i = 0; i < 5; i++) {
      clock += 1_000;
      vi.advanceTimersByTime(1_000);
    }
    expect(conn.state.status).toBe("connected");
    for (let i = 0; i < 3; i++) {
      clock += 1_000;
      vi.advanceTimersByTime(1_000);
    }
    expect(sockets[0]!.closed).toBe(true);
    expect(conn.state.status).not.toBe("connected");

    vi.advanceTimersByTime(60_000); // the retry opens a new socket, which never gets a welcome
    expect(sockets).toHaveLength(2);
    latest().serverOpen();
    clock += 16_000;
    vi.advanceTimersByTime(1_000);
    expect(sockets[1]!.closed).toBe(true);
  });

  it("stop() closes the socket and cancels pending retries", () => {
    conn.start();
    latest().serverClose();
    conn.stop();
    vi.advanceTimersByTime(60_000);
    expect(sockets).toHaveLength(1);
    expect(sockets[0]!.closed).toBe(true);
    expect(states.at(-1)?.status).toBe("reconnecting");
  });
});
