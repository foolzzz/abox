import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  outputDir: ".tmp/playwright-results",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: "list",
  expect: {
    timeout: 5_000
  },
  use: {
    baseURL: "http://127.0.0.1:4173",
    serviceWorkers: "block",
    trace: "retain-on-failure",
    ...devices["Desktop Chrome"],
    channel: "chrome"
  },
  webServer: {
    command: "npm run build && npm run preview -- --port 4173 --strictPort",
    url: "http://127.0.0.1:4173",
    reuseExistingServer: false,
    timeout: 120_000
  }
});
