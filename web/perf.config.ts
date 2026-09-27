import { defineConfig, devices } from "@playwright/test";

// Performance measurements against the running Compose stack; not part of CI.
export default defineConfig({
  testDir: "perf",
  testMatch: "*.perf.ts",
  workers: 1,
  reporter: "list",
  use: { baseURL: "http://localhost:8080", viewport: { width: 1280, height: 800 } },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"], viewport: { width: 1280, height: 800 } } }],
});
