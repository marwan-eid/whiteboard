import { defineConfig, devices } from "@playwright/test";

// End-to-end tests run against the full Compose stack (`docker compose up`),
// which serves the app on port 8080 by default.
export default defineConfig({
  testDir: "e2e",
  timeout: 30_000,
  retries: 0,
  // restart.spec.ts kills the shared node, so specs must not run concurrently.
  workers: 1,
  reporter: "list",
  use: {
    // E2E_BASE_URL runs the suite against a deployed site (skip the outage specs there).
    baseURL: process.env.E2E_BASE_URL ?? "http://localhost:8080",
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
