import { expect, test, type Browser, type Page } from "@playwright/test";

const visible = (p: Page) => p.evaluate(() => window.__whiteboard!.visible());

async function open(browser: Browser, board: string): Promise<Page> {
  const page = await (await browser.newContext()).newPage();
  await page.goto(`/b/${board}`);
  await expect(page.locator(".status")).toHaveAttribute("data-status", "connected", { timeout: 15_000 });
  await expect.poll(() => page.evaluate(() => window.__whiteboard!.ready()), { timeout: 15_000 }).toBe(true);
  return page;
}

/** Clicks the middle of a shape (the camera starts with board = screen coordinates). */
async function select(p: Page, s: { x: number; y: number; w: number; h: number }): Promise<void> {
  await p.keyboard.press("Escape");
  await p.mouse.click(s.x + s.w / 2, s.y + s.h / 2);
  await expect.poll(() => p.evaluate(() => window.__whiteboard!.selection().length)).toBe(1);
}

test("a dot vote: start, place and take back dots, end, and everyone sees the results", async ({ browser }) => {
  const board = `e2e-vote-${Date.now()}`;
  const a = await open(browser, board);
  const b = await open(browser, board);
  await a.mouse.dblclick(300, 300);
  await a.mouse.dblclick(650, 300);
  await expect.poll(async () => (await visible(b)).length).toBe(2);
  const [left, right] = (await visible(b)).sort((p, q) => p.x - q.x);

  await a.getByRole("button", { name: "🗳 Vote" }).click();
  await a.getByRole("spinbutton").fill("2");
  await a.getByRole("button", { name: "Start vote" }).click();
  await expect(b.getByTestId("dots-left")).toHaveText("2 of 2 dots left");

  // B puts both dots on the left shape, takes them back, then splits them.
  await select(b, left!);
  await b.getByRole("button", { name: "Vote for selection" }).click();
  await b.getByRole("button", { name: "Vote for selection" }).click();
  await expect(b.getByTestId("dots-left")).toHaveText("0 of 2 dots left");
  await expect(b.getByRole("button", { name: "Vote for selection" })).toBeDisabled();
  await b.getByRole("button", { name: "Take back" }).click();
  await expect(b.getByTestId("dots-left")).toHaveText("2 of 2 dots left");
  await b.getByRole("button", { name: "Vote for selection" }).click();
  await select(b, right!);
  await b.getByRole("button", { name: "Vote for selection" }).click();
  await expect(b.getByTestId("dots-left")).toHaveText("0 of 2 dots left");

  // A votes once for the left shape. A sees only its own dots while voting.
  await select(a, left!);
  await a.getByRole("button", { name: "Vote for selection" }).click();
  await expect(a.getByTestId("dots-left")).toHaveText("1 of 2 dots left");

  await a.getByRole("button", { name: "End vote and show results" }).click();
  for (const p of [a, b]) {
    await expect(p.getByTestId("vote-results").locator(".count")).toHaveText(["2", "1"]);
  }
  // Clicking a result selects its shape.
  await b.getByTestId("vote-results").getByRole("button").first().click();
  await expect.poll(() => b.evaluate(() => window.__whiteboard!.selection())).toEqual([left!.id]);
});
