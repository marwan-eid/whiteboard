import { expect, test, type Page } from "@playwright/test";

type Shape = { id: string; x: number; y: number; w: number; h: number };

const visible = (p: Page) => p.evaluate(() => window.__whiteboard!.visible());
const pending = (p: Page) => p.evaluate(() => window.__whiteboard!.pending());

async function open(browser: import("@playwright/test").Browser, board: string): Promise<Page> {
  const page = await (await browser.newContext()).newPage();
  await page.goto(`/b/${board}`);
  await expect(page.locator(".status")).toHaveAttribute("data-status", "connected", { timeout: 15_000 });
  return page;
}

test("two browsers see each other's creates, moves and deletes", async ({ browser }) => {
  const board = `e2e-${Date.now()}`;
  const a = await open(browser, board);
  const b = await open(browser, board);

  // A double-clicks empty canvas to create a shape; B sees it.
  await a.mouse.dblclick(400, 300);
  await expect.poll(async () => (await visible(b)).length).toBe(1);
  const [shape] = (await visible(b)) as [Shape];

  // B drags it 200px right and 100px down; A sees the final position.
  await b.mouse.move(shape.x + 20, shape.y + 20);
  await b.mouse.down();
  await b.mouse.move(shape.x + 220, shape.y + 120, { steps: 10 });
  await b.mouse.up();
  await expect.poll(async () => (await visible(a))[0]).toMatchObject({ x: shape.x + 200, y: shape.y + 100 });

  // A selects and deletes it; B sees it disappear.
  await a.mouse.click(shape.x + 220, shape.y + 120);
  await a.keyboard.press("Delete");
  await expect.poll(async () => (await visible(b)).length).toBe(0);

  // Everything was acknowledged.
  await expect.poll(() => pending(a)).toBe(0);
  await expect.poll(() => pending(b)).toBe(0);
});

test("a late joiner and a reloaded tab get the current board", async ({ browser }) => {
  const board = `e2e-late-${Date.now()}`;
  const a = await open(browser, board);
  await a.mouse.dblclick(300, 300);
  await a.mouse.dblclick(600, 300);
  await expect.poll(() => pending(a)).toBe(0);

  const late = await open(browser, board);
  await expect.poll(async () => (await visible(late)).length).toBe(2);

  await a.reload();
  await expect(a.locator(".status")).toHaveAttribute("data-status", "connected", { timeout: 15_000 });
  await expect.poll(async () => (await visible(a)).length).toBe(2);
});
