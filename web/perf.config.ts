import { defineConfig, devices } from "@playwright/test";

// Performance measurements against the running Compose stack; not part of CI.
export default defineConfig({
  testDir: "perf",
  testMatch: "*.perf.ts",
  workers: 1,
  reporter: "list",
  use: { baseURL: "http://localhost:8080", viewport: { width: 1280, height: 800 } },
  projects: [
    // Headless, with software WebGL (SwiftShader): comparable across machines, not representative.
    { name: "chromium", use: { ...devices["Desktop Chrome"], viewport: { width: 1280, height: 800 } } },
    // A real window on this machine's GPU: what docs/BENCHMARKS.md target 2 reports.
    { name: "gpu", use: { ...devices["Desktop Chrome"], viewport: { width: 1280, height: 800 }, headless: false } },
  ],
});
