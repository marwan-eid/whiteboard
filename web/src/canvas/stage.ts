import { Application, Texture, TilingSprite } from "pixi.js";
import type { Camera } from "./camera";

const GRID_SPACING = 24;
const GRID_MIN_ZOOM = 0.3;

/** Creates the WebGL stage with a dot grid that pans and zooms with the camera. */
export async function createStage(host: HTMLElement, camera: Camera): Promise<Application> {
  const app = new Application();
  await app.init({
    // Rendered on demand by FrameScheduler, not every frame.
    autoStart: false,
    resizeTo: host,
    background: "#f7f7f5",
    antialias: true,
    autoDensity: true,
    resolution: window.devicePixelRatio || 1,
  });
  host.appendChild(app.canvas);

  const grid = new TilingSprite({
    texture: dotTexture(),
    width: app.screen.width,
    height: app.screen.height,
  });
  app.stage.addChild(grid);
  const sync = () => {
    grid.visible = camera.zoom >= GRID_MIN_ZOOM;
    grid.tileScale.set(camera.zoom);
    grid.tilePosition.set(-camera.x * camera.zoom, -camera.y * camera.zoom);
  };
  camera.subscribe(sync);
  sync();
  app.renderer.on("resize", (width: number, height: number) => {
    grid.width = width;
    grid.height = height;
  });

  return app;
}

function dotTexture(): Texture {
  const canvas = document.createElement("canvas");
  canvas.width = canvas.height = GRID_SPACING;
  const ctx = canvas.getContext("2d");
  if (ctx) {
    ctx.fillStyle = "#c9ccd1";
    ctx.beginPath();
    ctx.arc(GRID_SPACING / 2, GRID_SPACING / 2, 1.2, 0, Math.PI * 2);
    ctx.fill();
  }
  return Texture.from(canvas);
}
