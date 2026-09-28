import { execFileSync } from "node:child_process";
import { expect, test, type Page } from "@playwright/test";

// Kills and restarts the Compose stack's node, so it must run alone (workers: 1).

const compose = (...args: string[]) => execFileSync("docker", ["compose", ...args], { stdio: "pipe" });
const visible = (p: Page) => p.evaluate(() => window.__whiteboard!.visible().length);
const pending = (p: Page) => p.evaluate(() => window.__whiteboard!.pending());
const status = (p: Page) => p.locator(".status");

/** Waits until the page is connected and its canvas takes input. */
async function connected(p: Page): Promise<void> {
  await expect(status(p)).toHaveAttribute("data-status", "connected", { timeout: 15_000 });
  await expect.poll(() => p.evaluate(() => window.__whiteboard!.ready()), { timeout: 15_000 }).toBe(true);
}

// A failed test must not leave the node down for the specs after it.
test.afterEach(() => {
  compose("up", "--detach", "--wait", "--wait-timeout", "60", "node-1");
});

test("unsynced edits survive a reload while offline and sync when the server is back", async ({ page, browser }) => {
  test.setTimeout(120_000);
  const board = `e2e-offline-${Date.now()}`;
  await page.goto(`/b/${board}`);
  await connected(page);
  await page.mouse.dblclick(200, 200);
  await expect.poll(() => pending(page)).toBe(0);

  compose("kill", "-s", "SIGKILL", "node-1");
  await expect(status(page)).not.toHaveAttribute("data-status", "connected", { timeout: 15_000 });
  await page.mouse.dblclick(450, 200);
  await page.mouse.dblclick(200, 450);
  expect(await pending(page)).toBe(2);

  // Reload with the server still down: the two unsynced shapes come back from IndexedDB.
  await page.reload();
  await expect.poll(() => pending(page)).toBe(2);
  expect(await visible(page)).toBe(2);
  await expect(status(page)).toContainText("2 unsynced");

  compose("up", "--detach", "--wait", "--wait-timeout", "60", "node-1");
  await expect(status(page)).toHaveAttribute("data-status", "connected", { timeout: 30_000 });
  await expect.poll(() => pending(page), { timeout: 15_000 }).toBe(0);
  await expect.poll(() => visible(page)).toBe(3);

  const other = await (await browser.newContext()).newPage();
  await other.goto(`/b/${board}`);
  await expect.poll(() => visible(other), { timeout: 15_000 }).toBe(3);

  // Nothing is left to resend: another reload shows the board without pending edits.
  await page.reload();
  await expect(status(page)).toHaveAttribute("data-status", "connected", { timeout: 15_000 });
  expect(await pending(page)).toBe(0);
  await expect.poll(() => visible(page)).toBe(3);
});

test("two tabs of one browser on the same board both stay connected", async ({ context }) => {
  const board = `e2e-tabs-${Date.now()}`;
  const a = await context.newPage();
  const b = await context.newPage();
  await a.goto(`/b/${board}`);
  await b.goto(`/b/${board}`);
  await connected(a);
  await connected(b);

  await a.mouse.dblclick(300, 300);
  await b.mouse.dblclick(600, 300);
  await expect.poll(() => visible(a)).toBe(2);
  await expect.poll(() => visible(b)).toBe(2);

  // Reloading one tab takes back its own id, not the other tab's.
  await b.reload();
  await connected(b);
  await expect(status(a)).toHaveAttribute("data-status", "connected");
  await b.mouse.dblclick(450, 500);
  await expect.poll(() => visible(a)).toBe(3);
});
