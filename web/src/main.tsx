import { render } from "preact";
import { boardIdFromPath, wsUrl } from "./board";
import { Camera } from "./canvas/camera";
import { Editor } from "./canvas/editor";
import { FrameScheduler } from "./canvas/frames";
import { Overlay } from "./canvas/overlay";
import { Scene } from "./canvas/scene";
import { createStage } from "./canvas/stage";
import { TextEditor } from "./canvas/textEditor";
import { Controller } from "./canvas/tools";
import { ShapeType } from "./gen/whiteboard/v1/protocol_pb";
import { Connection, type ConnectionHandlers } from "./net/connection";
import { randomClientId } from "./net/protocol";
import { isVisible } from "./sync/doc";
import { History } from "./sync/history";
import { Presence } from "./sync/presence";
import { SyncSession } from "./sync/session";
import { StatusBadge } from "./ui/StatusBadge";
import { Toolbar } from "./ui/Toolbar";

const boardId = boardIdFromPath(location.pathname);
const clientId = randomClientId();

// The connection delivers board traffic to the session, which sends through
// the connection; they are wired through these handlers.
const handlers: Partial<ConnectionHandlers> = {};
const connection = new Connection({ url: wsUrl(location), boardId, clientId, handlers });
const sync = new SyncSession({
  clientId,
  now: () => connection.serverNow(),
  send: (b) => connection.sendBatch(b),
  resync: () => connection.resync(),
});
const presence = new Presence((p) => connection.sendCursor(p.x, p.y));
handlers.onWelcome = (w) => {
  sync.onWelcome(w);
  presence.reset();
};
handlers.onFrame = (f) => {
  sync.onFrame(f);
  presence.apply(f.cursors);
};
connection.start();

const camera = new Camera();
const editor = new Editor();
const history = new History(sync);
const stageHost = document.getElementById("stage")!;
const uiHost = document.getElementById("ui")!;
const center = () => ({ x: stageHost.clientWidth / 2, y: stageHost.clientHeight / 2 });

render(
  <>
    <StatusBadge connection={connection} session={sync} presence={presence} boardId={boardId} />
    <Toolbar editor={editor} history={history} camera={camera} center={center} />
  </>,
  uiHost,
);

createStage(stageHost, camera)
  .then((app) => {
    const frames = new FrameScheduler(() => app.render());
    camera.subscribe(frames.request);
    app.renderer.on("resize", frames.request);
    const scene = new Scene(sync, camera, editor, frames.request);
    const overlay = new Overlay(scene, editor, camera, presence, frames);
    app.stage.addChild(scene.world, overlay.layer);
    const textEditor = new TextEditor(uiHost, sync, history, camera, editor);
    new Controller({ canvas: app.canvas, session: sync, history, scene, overlay, editor, camera, presence, textEditor });

    sync.subscribe((changed) => {
      scene.update(changed);
      if (changed === null) {
        // A new snapshot: drop selections of objects that no longer exist.
        editor.select([...editor.selection].filter((id) => scene.geometryOf(id)));
      }
      overlay.invalidate();
    });
    scene.update(null);
    hooks.geometry = (id) => {
      const g = scene.geometryOf(id);
      return g ? { bounds: g.bounds, line: g.line } : null;
    };
  })
  .catch((err: unknown) => {
    console.error("failed to start canvas", err);
  });

// Read-only hooks for end-to-end tests.
declare global {
  interface Window {
    __whiteboard?: {
      visible(): { id: string; type: string; x: number; y: number; w: number; h: number; text: string }[];
      pending(): number;
      selection(): string[];
      camera(): { x: number; y: number; zoom: number };
      cursors(): number;
      geometry(id: string): { bounds: { x: number; y: number; w: number; h: number }; line?: { x: number; y: number }[] } | null;
    };
  }
}
const hooks: NonNullable<Window["__whiteboard"]> = {
  visible: () =>
    [...sync.doc.values()].filter(isVisible).map((o) => ({
      id: o.id,
      type: ShapeType[o.props.type ?? 0] ?? "UNKNOWN",
      x: o.props.x ?? 0,
      y: o.props.y ?? 0,
      w: o.props.w ?? 0,
      h: o.props.h ?? 0,
      text: o.props.text ?? "",
    })),
  pending: () => sync.pendingCount,
  selection: () => [...editor.selection],
  camera: () => ({ x: camera.x, y: camera.y, zoom: camera.zoom }),
  cursors: () => presence.others.size,
  geometry: () => null,
};
window.__whiteboard = hooks;
