import { expect, test } from "@playwright/test";

test("app loads, renders a canvas, and connects to a node", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (err) => errors.push(err.message));

  await page.goto("/");
  await expect(page.locator("#stage canvas")).toBeVisible();
  await expect(page.locator(".status")).toHaveAttribute("data-status", "connected", { timeout: 15_000 });
  await expect(page.locator(".status")).toContainText("node-1");
  await expect(page.locator(".status")).toContainText("board: demo");
  // RTT appears after the first ping round trip.
  await expect(page.locator(".status")).toContainText(/\d+ ms/);

  expect(errors).toEqual([]);
});

test("board id comes from the /b/{id} path", async ({ page }) => {
  await page.goto("/b/team-retro");
  await expect(page.locator(".status")).toContainText("board: team-retro");
  await expect(page.locator(".status")).toHaveAttribute("data-status", "connected", { timeout: 15_000 });
});
