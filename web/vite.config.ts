import { defineConfig } from "vitest/config";

// In dev, Vite serves the app and proxies API/WebSocket traffic to a node
// started with `go run ./cmd/whiteboard` (see README).
const node = "http://localhost:8081";

export default defineConfig({
  server: {
    port: 5173,
    proxy: {
      "/ws": { target: node, ws: true },
      "/api": node,
      "/healthz": node,
    },
  },
  test: {
    environment: "node",
    // e2e/ holds Playwright specs, run separately against the Compose stack.
    include: ["src/**/*.test.ts"],
  },
});
