# ADR-0003: Canvas approach

- **Status:** Accepted
- **Date:** 2026-09-27

## Context
The client must:
- render boards with up to 100k objects, holding only the visible region in memory,
- apply our own sync protocol (ADR-0001).

The owner is a backend engineer and prefers minimal custom frontend work.

## Options
| Option | Pros | Cons |
|---|---|---|
| **tldraw SDK** | Polished editor UX out of the box. | Keeps the whole store in memory and has its own sync model. Production use needs a license key or a watermark. |
| **Excalidraw (MIT)** | Polished and open source. | Renders the whole scene on Canvas2D. Has its own collaboration model based on version numbers. Hard to adapt to viewport-only loading. |
| **Konva / Canvas2D scene graph** | Simple API. | Draw cost grows with the number of objects, and it slows down at tens of thousands. |
| **Custom renderer on PixiJS v8 (WebGL)** | Batched GPU rendering, fine at 100k simple shapes. Full control over data loading. | We build the editing tools ourselves. |

## Decision
**Build a custom, minimal editor on PixiJS v8**, with Preact for the UI chrome (toolbar, panels, history slider).

**v1 shape set:**
- rect
- ellipse
- sticky with text
- text
- arrow, which can bind to an object at either end
- freehand, with its points simplified before sending

**v1 editing:**
- select and multi-select
- move and resize
- pan and zoom
- local undo and redo

**Level of detail (LOD):** below a zoom threshold, the client switches to LOD mode. The server sends only `id`, bounding box, and fill color, and the client draws plain rectangles.

## Consequences
- **Less polish:** the editor is plainer than Excalidraw. Rotation, grouping, and image uploads are later scope.
- **Renderer is ours:** hit-testing, text layout, and zoom-dependent detail are our code, so they're covered by unit tests on the scene model.
- **UI stays thin:** the renderer is imperative and driven by the object store, not by React/Preact state.
