import { expect, test } from "@playwright/test";

// Records the README's load-test video: one browser on a board where
// simulated editors are drawing, with the live stats panel open.
//   1. go run ./cmd/loadgen -board gif-demo -editors 150 -hotspot 1 -area 1000 -duration 70s
//      (area 1000 centers the editors on the default view, around 500,500)
//   2. RECORD_BOARD=gif-demo npx playwright test -c perf.config.ts record.perf.ts --project=gpu
//   3. ffmpeg -ss 4 -t 10 -i test-results/<this test>/video.webm \
//        -vf "fps=8,scale=800:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=64[p];[b][p]paletteuse=dither=bayer:bayer_scale=5" \
//        ../docs/media/load-demo.gif

const BOARD = process.env.RECORD_BOARD ?? "gif-demo";

test.use({ video: { mode: "on", size: { width: 1280, height: 720 } }, viewport: { width: 1280, height: 720 } });

test("record a board under load", async ({ page }) => {
  test.setTimeout(120_000);
  await page.goto(`/b/${BOARD}`);
  await expect(page.locator(".status")).toHaveAttribute("data-status", "connected", { timeout: 15_000 });
  await page.getByRole("button", { name: "Live stats" }).click();
  await expect(page.getByTestId("stats")).toBeVisible({ timeout: 15_000 });
  await page.waitForTimeout(20_000);
});
