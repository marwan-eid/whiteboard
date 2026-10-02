import { expect, test, type Browser, type Page } from "@playwright/test";

const timer = (p: Page) => p.getByTestId("timer");
const seconds = async (p: Page) => {
  const [m, s] = (await timer(p).innerText()).split(":").map(Number);
  return m! * 60 + s!;
};

async function open(browser: Browser, board: string, clockOffsetMs = 0): Promise<Page> {
  const page = await (await browser.newContext()).newPage();
  // A wrong local clock must not change what the timer shows: it counts down
  // on the server's clock, estimated from pings.
  if (clockOffsetMs) await page.clock.install({ time: Date.now() + clockOffsetMs });
  await page.goto(`/b/${board}`);
  await expect(page.locator(".status")).toHaveAttribute("data-status", "connected", { timeout: 15_000 });
  return page;
}

test("a timer counts down in sync across browsers, even one with a wrong clock", async ({ browser }) => {
  const board = `e2e-timer-${Date.now()}`;
  const a = await open(browser, board);
  const b = await open(browser, board, 60 * 60_000); // an hour fast
  expect(await b.evaluate(() => Date.now()) - Date.now()).toBeGreaterThan(59 * 60_000);

  await a.getByRole("button", { name: "⏱ Timer" }).click();
  await a.getByRole("button", { name: "3 min" }).click();
  await expect(timer(b)).toBeVisible();
  // Both show the same remaining time, give or take the second boundary.
  for (let i = 0; i < 3; i++) {
    const [ta, tb] = await Promise.all([seconds(a), seconds(b)]);
    expect(ta).toBeGreaterThan(170);
    expect(ta).toBeLessThanOrEqual(180);
    expect(Math.abs(ta - tb)).toBeLessThanOrEqual(1);
    await a.waitForTimeout(700);
  }

  // Pausing stops it for everyone at the same value.
  await a.getByRole("button", { name: "Pause timer" }).click();
  await expect(b.locator(".timer")).toHaveAttribute("data-state", "paused");
  const paused = await timer(a).innerText();
  await expect(timer(b)).toHaveText(paused);
  await b.waitForTimeout(1500);
  await expect(timer(b)).toHaveText(paused);

  // A minute more, then resume and reset.
  await b.getByRole("button", { name: "Add a minute" }).click();
  await expect.poll(() => seconds(a)).toBeGreaterThan(220);
  await b.getByRole("button", { name: "Resume timer" }).click();
  await expect(a.locator(".timer")).toHaveAttribute("data-state", "running");
  await a.getByRole("button", { name: "Reset timer" }).click();
  await expect(timer(b)).toHaveCount(0);
});
