import type { Page } from "@playwright/test";

/**
 * Waits until the app has started. Its test hooks (window.__whiteboard) are
 * set only once it has its identity, which on a first visit takes a request
 * to a node, and goto() and reload() do not wait for that. Call this after
 * each of them, before touching the hooks: expect.poll does not retry a
 * callback that throws.
 */
export const started = (p: Page) => p.waitForFunction(() => window.__whiteboard !== undefined, undefined, { timeout: 30_000 });
