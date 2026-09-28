import { useEffect, useState } from "preact/hooks";
import type { Camera } from "../canvas/camera";
import { TOOLS, type Editor, type ToolId } from "../canvas/editor";
import type { History } from "../sync/history";

const ICONS: Record<ToolId, string> = {
  select: "↖",
  hand: "✋",
  rect: "▭",
  ellipse: "◯",
  sticky: "🗒",
  text: "T",
  arrow: "↗",
  pen: "✎",
};

/** Re-renders the component whenever the subscribed source changes. */
function useSubscription(subscribe: (fn: () => void) => () => void): void {
  const [, setTick] = useState(0);
  useEffect(() => subscribe(() => setTick((t) => t + 1)), [subscribe]);
}

export function Toolbar({
  editor,
  history,
  camera,
  center,
  onOpenHistory,
}: {
  editor: Editor;
  history: History;
  camera: Camera;
  center: () => { x: number; y: number };
  onOpenHistory: () => void;
}) {
  useSubscription((fn) => editor.subscribe(fn));
  useSubscription((fn) => history.subscribe(fn));
  useSubscription((fn) => camera.subscribe(fn));

  return (
    <>
      <div class="toolbar" role="toolbar" aria-label="Tools">
        {TOOLS.map((t) => (
          <button
            key={t.id}
            class={editor.tool === t.id ? "active" : ""}
            disabled={editor.readOnly && t.id !== "select" && t.id !== "hand"}
            title={`${t.label} (${t.key.toUpperCase()})`}
            aria-label={t.label}
            aria-pressed={editor.tool === t.id}
            data-tool={t.id}
            onClick={() => editor.setTool(t.id)}
          >
            {ICONS[t.id]}
          </button>
        ))}
        <span class="sep" />
        <button title="Undo (Ctrl+Z)" aria-label="Undo" disabled={editor.readOnly || !history.canUndo} onClick={() => history.undo()}>
          ↶
        </button>
        <button title="Redo (Ctrl+Shift+Z)" aria-label="Redo" disabled={editor.readOnly || !history.canRedo} onClick={() => history.redo()}>
          ↷
        </button>
        <span class="sep" />
        <button title="Version history" aria-label="Version history" data-tool="history" onClick={onOpenHistory}>
          🕘
        </button>
      </div>
      <div class="zoom">
        <button title="Zoom out (Ctrl+-)" aria-label="Zoom out" onClick={() => camera.zoomAt(center(), 1 / 1.25)}>
          −
        </button>
        <button title="Reset zoom (Ctrl+0)" class="zoom-level" onClick={() => camera.zoomAt(center(), 1 / camera.zoom)}>
          {Math.round(camera.zoom * 100)}%
        </button>
        <button title="Zoom in (Ctrl+=)" aria-label="Zoom in" onClick={() => camera.zoomAt(center(), 1.25)}>
          +
        </button>
      </div>
    </>
  );
}
