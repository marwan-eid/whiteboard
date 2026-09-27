import { Application, Texture, TilingSprite } from "pixi.js";

const GRID_SPACING = 24;

/** Creates the WebGL stage with an infinite dot-grid background. */
export async function createStage(host: HTMLElement): Promise<Application> {
  const app = new Application();
  await app.init({
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
