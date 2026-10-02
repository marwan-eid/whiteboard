import { expect, test } from "@playwright/test";

test("the live stats panel streams numbers through the proxy and counts edits", async ({ page }) => {
  await page.goto(`/b/e2e-stats-${Date.now()}`);
  await expect(page.locator(".status")).toHaveAttribute("data-status", "connected", { timeout: 15_000 });
  await expect.poll(() => page.evaluate(() => window.__whiteboard!.ready()), { timeout: 15_000 }).toBe(true);

  await page.getByRole("button", { name: "Live stats" }).click();
  const stats = page.getByTestId("stats");
  await expect(stats.locator("[data-stat=connections]")).not.toHaveText("0");
  // Usage counts (Postgres) are shown too; they refresh once a minute.
  await expect(stats.locator("[data-stat=guests]")).toBeVisible();

  // Draw clear of the panel (bottom left), or a click could land on its close button.
  for (let i = 0; i < 5; i++) await page.mouse.dblclick(450 + i * 120, 250);
  await expect.poll(() => page.evaluate(() => window.__whiteboard!.pending())).toBe(0);
  // Edits count once their second is complete; the rate is over 10 s.
  await expect.poll(async () => Number(await stats.locator("[data-stat=edits]").innerText()), { timeout: 5_000 }).toBeGreaterThan(0);
  await expect(stats).toContainText(/Your round trip, median.*\d+(\.\d)? ms/s);
  await expect(stats).toContainText(/\d+(\.\d)? \/ \d+(\.\d)? ms/);
});
