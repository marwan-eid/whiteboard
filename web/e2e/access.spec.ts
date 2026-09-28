import { expect, test, type Browser, type Page } from "@playwright/test";

const visible = (p: Page) => p.evaluate(() => window.__whiteboard!.visible());
const status = (p: Page) => p.locator(".status");

/** A page in a fresh browser context: a different guest identity. */
async function guest(browser: Browser, url: string): Promise<Page> {
  const page = await (await browser.newContext()).newPage();
  await page.goto(url);
  return page;
}

async function createLink(owner: Page, role: "view" | "edit"): Promise<string> {
  await owner.getByRole("button", { name: `Create ${role} link` }).click();
  const input = owner.getByTestId("share-url");
  await expect(input).toHaveValue(/#k=/);
  return input.inputValue();
}

test("private boards: owner, strangers, view and edit links, revocation", async ({ browser }) => {
  // A guest creates a private board from the demo board.
  const owner = await guest(browser, "/");
  await expect(status(owner)).toHaveAttribute("data-status", "connected", { timeout: 15_000 });
  await owner.getByRole("button", { name: "Boards" }).click();
  await owner.getByRole("button", { name: "New private board" }).click();
  await expect(owner).toHaveURL(/\/b\/[\w-]+$/);
  await expect(status(owner)).toHaveAttribute("data-status", "connected", { timeout: 15_000 });
  const boardUrl = owner.url();
  await owner.mouse.dblclick(400, 400);
  await expect.poll(async () => (await visible(owner)).length).toBe(1);

  // It is listed under the owner's boards; the owner sees Share.
  await owner.getByRole("button", { name: "Share" }).click();
  await expect(owner.locator(".board-menu li a[aria-current=page]")).toBeVisible();

  // A stranger without a link is turned away.
  const stranger = await guest(browser, boardUrl);
  await expect(status(stranger)).toHaveAttribute("data-status", "rejected", { timeout: 15_000 });
  await expect(status(stranger)).toContainText("no access");

  // A view link: sees the board, cannot edit.
  const viewUrl = await createLink(owner, "view");
  const viewer = await guest(browser, viewUrl);
  await expect(status(viewer)).toHaveAttribute("data-status", "connected", { timeout: 15_000 });
  await expect(viewer.getByTestId("view-only")).toBeVisible();
  await expect.poll(async () => (await visible(viewer)).length).toBe(1);
  await viewer.mouse.dblclick(200, 200);
  await expect(viewer.getByRole("button", { name: "Rectangle" })).toBeDisabled();
  expect(await visible(viewer)).toHaveLength(1);
  await expect(status(viewer)).toHaveAttribute("data-status", "connected");

  // An edit link: can draw, and the owner sees it.
  const editUrl = await createLink(owner, "edit");
  const editor = await guest(browser, editUrl);
  await expect(status(editor)).toHaveAttribute("data-status", "connected", { timeout: 15_000 });
  await editor.mouse.dblclick(700, 250);
  await expect.poll(async () => (await visible(owner)).length).toBe(2);

  // Revoking the view link disconnects the viewer at once; the editor stays.
  await owner.locator(".board-menu li", { hasText: "View link" }).getByRole("button", { name: "Revoke" }).click();
  await expect(status(viewer)).toHaveAttribute("data-status", "rejected");
  await expect(status(viewer)).toContainText("access revoked");
  await viewer.reload();
  await expect(status(viewer)).toHaveAttribute("data-status", "rejected", { timeout: 15_000 });
  await expect(status(editor)).toHaveAttribute("data-status", "connected");
});
