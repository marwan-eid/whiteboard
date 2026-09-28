import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import {
  ClientMessageSchema,
  ServerMessageSchema,
  type ClientMessage,
  type OpBatch,
  type ServerMessage,
} from "../gen/whiteboard/v1/protocol_pb";

/** Keep in sync with protocol.Version in internal/protocol/protocol.go. */
export const PROTOCOL_VERSION = 1;

/** Largest client id: ids must stay exact as JS numbers (internal/protocol.MaxClientID). */
export const MAX_CLIENT_ID = 2 ** 53 - 1;

export interface ViewportRect {
  x: number;
  y: number;
  w: number;
  h: number;
  lod: boolean;
}

export function encodeHello(boardId: string, clientId: number, viewport?: ViewportRect): Uint8Array<ArrayBuffer> {
  return encode(
    create(ClientMessageSchema, {
      msg: { case: "hello", value: { protocolVersion: PROTOCOL_VERSION, boardId, clientId: BigInt(clientId), viewport } },
    }),
  );
}

export function encodeViewport(v: ViewportRect): Uint8Array<ArrayBuffer> {
  return encode(create(ClientMessageSchema, { msg: { case: "viewport", value: v } }));
}

export function encodeTimePing(t0: number): Uint8Array<ArrayBuffer> {
  return encode(create(ClientMessageSchema, { msg: { case: "timePing", value: { t0 } } }));
}

export function encodeOpBatch(batch: OpBatch): Uint8Array<ArrayBuffer> {
  return encode(create(ClientMessageSchema, { msg: { case: "opBatch", value: batch } }));
}

export function encodeCursor(x: number, y: number): Uint8Array<ArrayBuffer> {
  return encode(create(ClientMessageSchema, { msg: { case: "cursor", value: { x, y } } }));
}

export function encodeHistoryRequest(seq: number): Uint8Array<ArrayBuffer> {
  return encode(create(ClientMessageSchema, { msg: { case: "history", value: { seq: BigInt(seq) } } }));
}

export function encodeRestore(seq: number): Uint8Array<ArrayBuffer> {
  return encode(create(ClientMessageSchema, { msg: { case: "restore", value: { seq: BigInt(seq) } } }));
}

export function decodeServerMessage(data: ArrayBuffer): ServerMessage {
  return fromBinary(ServerMessageSchema, new Uint8Array(data));
}

function encode(msg: ClientMessage): Uint8Array<ArrayBuffer> {
  // Copy into a plain ArrayBuffer-backed view; WebSocket.send's DOM typing
  // does not accept views over SharedArrayBuffer.
  return new Uint8Array(toBinary(ClientMessageSchema, msg));
}

/** A cryptographically random client id in [1, MAX_CLIENT_ID]. */
export function randomClientId(): number {
  const [hi, lo] = crypto.getRandomValues(new Uint32Array(2));
  const id = (hi! & 0x1f_ffff) * 2 ** 32 + lo!;
  return id === 0 ? 1 : id;
}
