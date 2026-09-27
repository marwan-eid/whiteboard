import { render } from "preact";
import { boardIdFromPath, wsUrl } from "./board";
import { Interaction } from "./canvas/interaction";
import { Scene } from "./canvas/scene";
import { createStage } from "./canvas/stage";
import { Connection, type ConnectionHandlers } from "./net/connection";
import { randomClientId } from "./net/protocol";
import { isVisible } from "./sync/doc";
import { SyncSession } from "./sync/session";
import { StatusBadge } from "./ui/StatusBadge";

const boardId = boardIdFromPath(location.pathname);
const clientId = randomClientId();

// The connection and the session reference each other: the connection
// delivers board traffic to the session, which sends through the connection.
const handlers: Partial<ConnectionHandlers> = {};
const connection = new Connection({ url: wsUrl(location), boardId, clientId, handlers });
const sync = new SyncSession({
  clientId,
  now: () => connection.serverNow(),
  send: (b) => connection.sendBatch(b),
  resync: () => connection.resync(),
});
handlers.onWelcome = (w) => sync.onWelcome(w);
handlers.onFrame = (f) => sync.onFrame(f);
connection.start();

render(<StatusBadge connection={connection} session={sync} boardId={boardId} />, document.getElementById("ui")!);

createStage(document.getElementById("stage")!)
  .then((app) => {
    const scene = new Scene(sync, (id, e) => interaction.onShapePointerDown(id, e));
    app.stage.addChild(scene.layer);
    const interaction = new Interaction(app, sync, scene);
    sync.subscribe((changed) => scene.update(changed));
    scene.update(null);
  })
  .catch((err: unknown) => {
    console.error("failed to start canvas", err);
  });

// Read-only hooks for end-to-end tests.
declare global {
  interface Window {
    __whiteboard?: {
      visible(): { id: string; x: number; y: number; w: number; h: number }[];
      pending(): number;
    };
  }
}
window.__whiteboard = {
  visible: () =>
    [...sync.doc.values()].filter(isVisible).map((o) => ({
      id: o.id,
      x: o.props.x ?? 0,
      y: o.props.y ?? 0,
      w: o.props.w ?? 0,
      h: o.props.h ?? 0,
    })),
  pending: () => sync.pendingCount,
};
