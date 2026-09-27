import { expect, test, type Browser, type Page } from "@playwright/test";

// The usable-editor milestone (W3): every tool, selection, resize, undo/redo,
// pan/zoom and live cursors, checked through the real stack.

const visible = (p: Page) => p.evaluate(() => window.__whiteboard!.visible());
const byType = async (p: Page, type: string) => (await visible(p)).filter((o) => o.type === type);
const settled = (p: Page) => expect.poll(() => p.evaluate(() => window.__whiteboard!.pending())).toBe(0);

async function open(browser: Browser, board: string): Promise<Page> {
  const page = await (await browser.newContext({ viewport: { width: 1280, height: 800 } })).newPage();
  await page.goto(`/b/${board}`);
  await expect(page.locator(".status")).toHaveAttribute("data-status", "connected", { timeout: 15_000 });
  return page;
}

async function drag(p: Page, from: [number, number], to: [number, number]): Promise<void> {
  await p.mouse.move(...from);
  await p.mouse.down();
  await p.mouse.move(...to, { steps: 8 });
  await p.mouse.up();
}

const tool = (p: Page, id: string) => p.locator(`.toolbar button[data-tool="${id}"]`).click();

test("shape tools create objects others see", async ({ browser }) => {
  const board = `e2e-tools-${Date.now()}`;
  const a = await open(browser, board);
  const b = await open(browser, board);

  await tool(a, "rect");
  await drag(a, [100, 100], [300, 220]);
  await tool(a, "ellipse");
  await a.mouse.click(500, 160); // a click makes a default-size shape
  await tool(a, "pen");
  await drag(a, [100, 400], [300, 450]);

  await expect.poll(async () => (await byType(b, "RECT"))[0]).toMatchObject({ x: 100, y: 100, w: 200, h: 120 });
  await expect.poll(async () => (await byType(b, "ELLIPSE"))[0]).toMatchObject({ w: 140, h: 100 });
  await expect.poll(async () => (await byType(b, "FREEHAND")).length).toBe(1);
  // After a shape tool, the tool returns to select; the pen stays active.
  await expect(a.locator('.toolbar button[data-tool="pen"]')).toHaveClass(/active/);
});

test("sticky notes and text are edited in place", async ({ browser }) => {
  const board = `e2e-text-${Date.now()}`;
  const a = await open(browser, board);
  const b = await open(browser, board);

  await tool(a, "sticky");
  await a.mouse.click(300, 300);
  await a.keyboard.type("Ship W3");
  await a.keyboard.press("Escape");
  await expect.poll(async () => (await byType(b, "STICKY"))[0]?.text).toBe("Ship W3");

  await tool(a, "text");
  await a.mouse.click(700, 300);
  await a.keyboard.type("Title");
  await a.keyboard.press("Control+Enter");
  await expect.poll(async () => (await byType(b, "TEXT"))[0]?.text).toBe("Title");

  // Double-click edits existing text; an emptied text object is removed.
  const t = (await byType(a, "TEXT"))[0]!;
  await a.mouse.dblclick(t.x + 20, t.y + 15);
  await a.keyboard.press("Control+A");
  await a.keyboard.press("Delete");
  await a.keyboard.press("Escape");
  await expect.poll(async () => (await byType(b, "TEXT")).length).toBe(0);
});

test("arrows attach to shapes and follow them", async ({ browser }) => {
  const board = `e2e-arrow-${Date.now()}`;
  const a = await open(browser, board);
  const b = await open(browser, board);

  await tool(a, "rect");
  await drag(a, [100, 100], [260, 200]);
  await tool(a, "rect");
  await drag(a, [500, 100], [660, 200]);
  await tool(a, "arrow");
  await drag(a, [180, 150], [580, 150]); // center to center
  await settled(a);

  const arrow = (await byType(b, "ARROW"))[0]!;
  const ends = async (p: Page) => (await p.evaluate((id) => window.__whiteboard!.geometry(id), arrow.id))?.line;
  // Ends stop at the rectangles' borders.
  await expect.poll(() => ends(b)).toEqual([
    { x: 260, y: 150 },
    { x: 500, y: 150 },
  ]);

  // Move the right rectangle down; the arrow's end follows on both screens.
  await drag(a, [600, 130], [600, 330]);
  await expect.poll(async () => (await ends(b))?.[1]?.y).toBeGreaterThan(250);
});

test("select, move, resize, delete, undo and redo", async ({ browser }) => {
  const board = `e2e-select-${Date.now()}`;
  const a = await open(browser, board);

  await tool(a, "rect");
  await drag(a, [100, 100], [200, 200]);
  await tool(a, "rect");
  await drag(a, [300, 100], [400, 200]);
  const rects = () => byType(a, "RECT");

  // Marquee both, move both by (+50, +100).
  await a.mouse.click(700, 600); // deselect
  await drag(a, [80, 80], [420, 220]);
  expect(await a.evaluate(() => window.__whiteboard!.selection().length)).toBe(2);
  await drag(a, [150, 150], [200, 250]);
  await expect.poll(async () => (await rects()).map((r) => [r.x, r.y]).sort()).toEqual([
    [150, 200],
    [350, 200],
  ]);

  // Undo restores both in one step; redo moves them again.
  await a.keyboard.press("Control+z");
  await expect.poll(async () => (await rects()).map((r) => r.y)).toEqual([100, 100]);
  await a.keyboard.press("Control+Shift+z");
  await expect.poll(async () => (await rects()).map((r) => r.y)).toEqual([200, 200]);

  // Resize one from its bottom-right handle.
  await a.mouse.click(700, 600);
  await a.mouse.click(175, 250); // select the left one (now at 150,200 100x100)
  await drag(a, [253, 303], [303, 353]);
  await expect.poll(async () => (await rects()).find((r) => r.x === 150)).toMatchObject({ w: 150, h: 150 });

  // Delete, then undo the delete.
  await a.keyboard.press("Delete");
  await expect.poll(async () => (await rects()).length).toBe(1);
  await a.keyboard.press("Control+z");
  await expect.poll(async () => (await rects()).length).toBe(2);
  await settled(a);
});

test("pan and zoom keep board coordinates consistent", async ({ browser }) => {
  const a = await open(browser, `e2e-camera-${Date.now()}`);
  await a.mouse.move(640, 400);
  await a.keyboard.down("Control");
  await a.mouse.wheel(0, -300); // zoom in around the pointer
  await a.keyboard.up("Control");
  const cam = await a.evaluate(() => window.__whiteboard!.camera());
  expect(cam.zoom).toBeGreaterThan(1.5);

  // A rectangle drawn on screen lands at the matching board coordinates.
  await tool(a, "rect");
  await drag(a, [640, 400], [740, 500]);
  const r = (await byType(a, "RECT"))[0]!;
  expect(r.x).toBeCloseTo(cam.x + 640 / cam.zoom, 0);
  expect(r.w).toBeCloseTo(100 / cam.zoom, 0);

  // Plain wheel pans.
  await a.mouse.wheel(0, 200);
  expect((await a.evaluate(() => window.__whiteboard!.camera())).y).toBeCloseTo(cam.y + 200 / cam.zoom, 3);
});

test("live cursors appear and leave", async ({ browser }) => {
  const board = `e2e-cursor-${Date.now()}`;
  const a = await open(browser, board);
  const b = await open(browser, board);
  await a.mouse.move(300, 300);
  await a.mouse.move(320, 310);
  await expect.poll(() => b.evaluate(() => window.__whiteboard!.cursors())).toBe(1);
  await expect(b.locator(".status")).toContainText("1 other here");
  await a.close();
  await expect.poll(() => b.evaluate(() => window.__whiteboard!.cursors())).toBe(0);
});
