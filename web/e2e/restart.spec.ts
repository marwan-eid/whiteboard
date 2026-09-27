import { execFileSync } from "node:child_process";
import { expect, test, type Page } from "@playwright/test";

// Kills and restarts the Compose stack's node, so it must run alone (workers: 1).

const compose = (...args: string[]) => execFileSync("docker", ["compose", ...args], { stdio: "pipe" });
const visible = (p: Page) => p.evaluate(() => window.__whiteboard!.visible().length);
const pending = (p: Page) => p.evaluate(() => window.__whiteboard!.pending());

test("edits made while the node is down are kept and synced after it restarts", async ({ page, browser }) => {
  test.setTimeout(120_000);
  const board = `e2e-restart-${Date.now()}`;
  await page.goto(`/b/${board}`);
  const status = page.locator(".status");
  await expect(status).toHaveAttribute("data-status", "connected", { timeout: 15_000 });

  await page.mouse.dblclick(200, 200);
  await page.mouse.dblclick(450, 200);
  await expect.poll(() => pending(page)).toBe(0);

  compose("kill", "-s", "SIGKILL", "node-1");
  await expect(status).not.toHaveAttribute("data-status", "connected", { timeout: 15_000 });

  // Edit while disconnected: they show at once and count as unsynced.
  await page.mouse.dblclick(200, 450);
  await page.mouse.dblclick(450, 450);
  expect(await visible(page)).toBe(4);
  expect(await pending(page)).toBe(2);
  await expect(status).toContainText("2 unsynced");

  compose("up", "--detach", "--wait", "--wait-timeout", "60", "node-1");
  await expect(status).toHaveAttribute("data-status", "connected", { timeout: 30_000 });
  await expect.poll(() => pending(page), { timeout: 15_000 }).toBe(0);

  // A new visitor sees all four shapes, from the restarted node.
  const other = await (await browser.newContext()).newPage();
  await other.goto(`/b/${board}`);
  await expect.poll(() => visible(other), { timeout: 15_000 }).toBe(4);
});
