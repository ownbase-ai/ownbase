import { defineConfig, devices } from "@playwright/test";

// Tier-B: real ownbasectl via the local bridge. Local/dev only.
// Requires: unlocked path is NOT needed — the bridge uses an isolated HOME.
// Build the sidecar first: npm run sidecar
// Then: npm run e2e:real

const PORT = 5273;
const BRIDGE = Number(process.env.E2E_BRIDGE_PORT || 7391);

export default defineConfig({
  testDir: "./e2e/tests-real",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: "list",
  timeout: 120_000,
  expect: { timeout: 30_000 },
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: "on-first-retry",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: [
    {
      command: "npm run dev -- --host 127.0.0.1 --port 5273",
      url: `http://127.0.0.1:${PORT}`,
      reuseExistingServer: !process.env.CI,
      timeout: 120_000,
    },
    {
      command: `node e2e/bridge/server.mjs`,
      url: `http://127.0.0.1:${BRIDGE}/health`,
      reuseExistingServer: !process.env.CI,
      timeout: 30_000,
      env: {
        ...process.env,
        E2E_BRIDGE_PORT: String(BRIDGE),
      },
    },
  ],
});
