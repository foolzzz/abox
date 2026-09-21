import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e-local",
  outputDir: ".tmp/playwright-local-results",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: "list",
  expect: { timeout: 10_000 },
  use: {
    baseURL: process.env.AGENTBOX_E2E_BASE_URL ?? "http://127.0.0.1:8080",
    trace: "retain-on-failure",
    ...devices["Desktop Chrome"],
    channel: "chrome"
  }
});
