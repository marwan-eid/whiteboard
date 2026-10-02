import { render } from "preact";
import { boardIdFromPath, shareTokenFromHash, wsUrl } from "./board";
import { Camera } from "./canvas/camera";
import { Editor } from "./canvas/editor";
import { FrameScheduler } from "./canvas/frames";
import { Overlay } from "./canvas/overlay";
import { Scene } from "./canvas/scene";
import { createStage } from "./canvas/stage";
import { TextEditor } from "./canvas/textEditor";
import { ViewportTracker } from "./canvas/viewport";
import { Controller } from "./canvas/tools";
import { Role, ShapeType } from "./gen/whiteboard/v1/protocol_pb";
import { Api } from "./net/api";
import { Connection, type ConnectionHandlers } from "./net/connection";
import { isShape, type Doc } from "./sync/doc";
import { History } from "./sync/history";
import { HistoryView } from "./sync/historyView";
import { acquireIdentity } from "./sync/persist";
import { Presence } from "./sync/presence";
import { SyncSession } from "./sync/session";
import { BoardMenu } from "./ui/BoardMenu";
import { HistoryPanel } from "./ui/HistoryPanel";
import { StatusBadge } from "./ui/StatusBadge";
import { TimerWidget } from "./ui/TimerWidget";
import { VotePanel } from "./ui/VotePanel";
import { Toolbar } from "./ui/Toolbar";

const boardId = boardIdFromPath(location.pathname);
// A share link carries its token in the URL fragment, which never reaches server logs.
const shareToken = shareTokenFromHash(location.hash);
const api = new Api();
// This tab's client id on the board, with edits an earlier page left unsynced;
// and who we are (a first visit asks the server for a guest identity).
const [identity, guestToken] = await Promise.all([acquireIdentity(boardId), api.guestToken()]);
const clientId = identity.clientId;

// The connection delivers board traffic to the session, which sends through
// the connection; they are wired through these handlers.
const camera = new Camera();
const stageHost = document.getElementById("stage")!;
const uiHost = document.getElementById("ui")!;
const viewport = new ViewportTracker(camera, () => ({ w: stageHost.clientWidth || window.innerWidth, h: stageHost.clientHeight || window.innerHeight }));

const handlers: Partial<ConnectionHandlers> = {};
const connection = new Connection({
  url: wsUrl(location),
  boardId,
  clientId,
  handlers,
  viewport: () => viewport.current,
  credentials: () => ({ guestToken, shareToken }),
});
viewport.subscribe(() => connection.sendViewport());
window.addEventListener("resize", () => viewport.check());
const sync = new SyncSession({
  clientId,
  now: () => connection.serverNow(),
  send: (b) => connection.sendBatch(b),
  resync: () => connection.resync(),
  store: identity.store,
  restored: identity.restored,
});
const presence = new Presence((p) => connection.sendCursor(p.x, p.y));
const editor = new Editor();
let role = Role.UNSPECIFIED;
handlers.onWelcome = (w) => {
  sync.onWelcome(w);
  presence.reset();
  if (w.role !== role) {
    role = w.role;
    editor.setViewOnly(role === Role.VIEWER);
    renderUI();
  }
};
handlers.onFrame = (f) => {
  sync.onFrame(f);
  presence.apply(f.cursors);
};
const historyView = new HistoryView({
  request: (s) => connection.requestHistory(s),
  restore: (s) => connection.restore(s),
  head: () => sync.seq,
});
handlers.onHistory = (h) => historyView.onHistory(h);
connection.start();

const history = new History(sync);
const center = () => ({ x: stageHost.clientWidth / 2, y: stageHost.clientHeight / 2 });

// Vote dots to draw on shapes; the overlay starts asynchronously, so keep the latest.
let badges: ReadonlyMap<string, number> = new Map();
let overlayRef: Overlay | null = null;
const onBadges = (b: ReadonlyMap<string, number>) => {
  badges = b;
  overlayRef?.setBadges(b);
};

function renderUI(): void {
  render(
    <>
      <StatusBadge connection={connection} session={sync} presence={presence} boardId={boardId} viewOnly={role === Role.VIEWER} />
      <Toolbar editor={editor} history={history} camera={camera} center={center} onOpenHistory={() => historyView.open()} />
      <HistoryPanel view={historyView} canRestore={role !== Role.VIEWER} />
      <BoardMenu api={api} boardId={boardId} role={role} />
      <TimerWidget session={sync} now={() => connection.serverNow()} canEdit={role !== Role.VIEWER} />
      <VotePanel session={sync} editor={editor} canEdit={role !== Role.VIEWER} onBadges={onBadges} />
    </>,
    uiHost,
  );
}
renderUI();

createStage(stageHost, camera)
  .then((app) => {
    const frames = new FrameScheduler(() => app.render());
    camera.subscribe(frames.request);
    app.renderer.on("resize", frames.request);
    const scene = new Scene(sync, camera, editor, frames);
    scene.setLOD(viewport.lod);
    viewport.subscribe((v) => scene.setLOD(v.lod));
    const overlay = new Overlay(scene, editor, camera, presence, frames);
    overlayRef = overlay;
    overlay.setBadges(badges);
    app.stage.addChild(scene.world, overlay.layer);
    const textEditor = new TextEditor(uiHost, sync, history, camera, editor);
    new Controller({ canvas: app.canvas, session: sync, history, scene, overlay, editor, camera, presence, textEditor });

    // History mode shows a past version read-only; live edits keep syncing underneath.
    let shown: Doc | null = null;
    historyView.subscribe(() => {
      editor.setReadOnly(historyView.active);
      const next = historyView.active ? (historyView.doc ?? shown) : null;
      if (next !== shown) {
        shown = next;
        scene.setOverride(next);
      }
      overlay.invalidate();
    });

    sync.subscribe((changed) => {
      if (!historyView.active) scene.update(changed);
      if (changed === null) {
        // A new snapshot: drop selections of objects that no longer exist.
        editor.select([...editor.selection].filter((id) => scene.geometryOf(id)));
      }
      overlay.invalidate();
    });
    scene.update(null);
    hooks.frameTimes = () => frames.recent.splice(0);
    hooks.ready = () => true;
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
      lod(): boolean;
      /** True once the canvas takes input (it starts asynchronously, maybe after the Welcome). */
      ready(): boolean;
      frameTimes(): number[];
      history(): { active: boolean; seq: number; head: number; shown: string[] | null };
      geometry(id: string): { bounds: { x: number; y: number; w: number; h: number }; line?: { x: number; y: number }[] } | null;
    };
  }
}
const hooks: NonNullable<Window["__whiteboard"]> = {
  visible: () =>
    [...sync.doc.values()].filter(isShape).map((o) => ({
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
  lod: () => viewport.lod,
  ready: () => false,
  frameTimes: () => [],
  history: () => ({
    active: historyView.active,
    seq: historyView.seq,
    head: historyView.head,
    shown: historyView.doc ? [...historyView.doc.values()].filter(isShape).map((o) => o.id) : null,
  }),
  geometry: () => null,
};
window.__whiteboard = hooks;
