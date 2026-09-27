import { expect, test, type Page } from "@playwright/test";
import { writeFileSync } from "node:fs";

// Measures a large seeded board in the browser (docs/BENCHMARKS.md, target 2).
// Seed first:   go run ./cmd/seed -board big100k -objects 100000
// Then run:     npx playwright test -c perf.config.ts
// Results go to perf-results.json. Numbers depend heavily on hardware and on
// whether the browser has a GPU; always report them with that context.

const BOARD = process.env.PERF_BOARD ?? "big100k";

const hook = <T>(p: Page, name: string) => p.evaluate((n) => (window.__whiteboard as unknown as Record<string, () => T>)[n]!(), name);

function stats(xs: number[]) {
  const s = [...xs].sort((a, b) => a - b);
  const q = (p: number) => s[Math.min(s.length - 1, Math.floor(p * s.length))] ?? 0;
  return { frames: s.length, p50_ms: +q(0.5).toFixed(2), p95_ms: +q(0.95).toFixed(2), p99_ms: +q(0.99).toFixed(2), max_ms: +(s.at(-1) ?? 0).toFixed(2) };
}

test("large board: load, pan, zoom out to LOD", async ({ page }) => {
  test.setTimeout(180_000);
  const t0 = Date.now();
  await page.goto(`/b/${BOARD}`);
  await expect.poll(() => hook<unknown[]>(page, "visible").then((v) => v.length), { timeout: 60_000 }).toBeGreaterThan(0);
  const firstObjectsMs = Date.now() - t0;
  await page.waitForTimeout(1000);
  const heldAtStart = (await hook<unknown[]>(page, "visible")).length;
  await hook(page, "frameTimes"); // discard load frames

  // Pan across the board: 120 wheel steps of 40px.
  await page.mouse.move(640, 400);
  for (let i = 0; i < 120; i++) await page.mouse.wheel(40, 25);
  await page.waitForTimeout(1500);
  const pan = stats(await hook<number[]>(page, "frameTimes"));
  const heldAfterPan = (await hook<unknown[]>(page, "visible")).length;

  // Zoom far out until LOD kicks in, then pan there.
  await page.keyboard.down("Control");
  for (let i = 0; i < 40 && !(await hook<boolean>(page, "lod")); i++) await page.mouse.wheel(0, 120);
  await page.keyboard.up("Control");
  await page.waitForTimeout(3000);
  const zoomOut = stats(await hook<number[]>(page, "frameTimes"));
  const heldInLOD = (await hook<unknown[]>(page, "visible")).length;
  for (let i = 0; i < 60; i++) await page.mouse.wheel(60, 40);
  await page.waitForTimeout(1500);
  const lodPan = stats(await hook<number[]>(page, "frameTimes"));

  const gpu = await page.evaluate(() => {
    const g = document.createElement("canvas").getContext("webgl2");
    const d = g?.getExtension("WEBGL_debug_renderer_info");
    return d ? String(g!.getParameter(d.UNMASKED_RENDERER_WEBGL)) : "unknown";
  });
  const result = {
    board: BOARD,
    renderer: gpu,
    viewport: page.viewportSize(),
    first_objects_ms: firstObjectsMs,
    objects_held: { start: heldAtStart, after_pan: heldAfterPan, lod: heldInLOD },
    frame_ms: { pan, zoom_out: zoomOut, lod_pan: lodPan },
  };
  console.log(JSON.stringify(result, null, 2));
  writeFileSync("perf-results.json", JSON.stringify(result, null, 2));
});
