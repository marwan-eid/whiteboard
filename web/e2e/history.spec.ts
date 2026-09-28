import { expect, test, type Browser, type Page } from "@playwright/test";

const visible = (p: Page) => p.evaluate(() => window.__whiteboard!.visible().length);
const hist = (p: Page) => p.evaluate(() => window.__whiteboard!.history());

async function open(browser: Browser, board: string): Promise<Page> {
  const page = await (await browser.newContext()).newPage();
  await page.goto(`/b/${board}`);
  await expect(page.locator(".status")).toHaveAttribute("data-status", "connected", { timeout: 15_000 });
  return page;
}

test("scrub history, restore a version, and undo the restore", async ({ browser }) => {
  const board = `e2e-history-${Date.now()}`;
  const a = await open(browser, board);
  const b = await open(browser, board);

  // Three shapes: versions 1, 2 and 3.
  for (const x of [200, 450, 700]) {
    await a.mouse.dblclick(x, 300);
    await expect.poll(() => a.evaluate(() => window.__whiteboard!.pending())).toBe(0);
  }
  await expect.poll(() => visible(b)).toBe(3);

  // Open history and go back to version 1: one shape, read-only.
  await a.locator('.toolbar button[data-tool="history"]').click();
  await expect(a.locator(".history")).toBeVisible();
  await expect.poll(async () => (await hist(a)).head).toBe(3);
  await a.locator('.history input[type="range"]').fill("1");
  await expect.poll(async () => (await hist(a)).shown?.length).toBe(1);
  await a.mouse.dblclick(900, 500); // ignored: history mode is read-only
  await expect(a.locator(".history")).toContainText("Version 1 of 3");

  // Restore it: both browsers go back to one shape.
  await a.getByRole("button", { name: "Restore this version" }).click();
  await expect(a.locator(".history")).toBeHidden();
  await expect.poll(() => visible(a)).toBe(1);
  await expect.poll(() => visible(b)).toBe(1);

  // The restore is version 4; restoring version 3 undoes it.
  await b.locator('.toolbar button[data-tool="history"]').click();
  await expect.poll(async () => (await hist(b)).head).toBe(4);
  await b.locator('.history input[type="range"]').fill("3");
  await expect.poll(async () => (await hist(b)).shown?.length).toBe(3);
  await b.getByRole("button", { name: "Restore this version" }).click();
  await expect.poll(() => visible(a)).toBe(3);
  await expect.poll(() => visible(b)).toBe(3);
});
