import { readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { expect, test } from "@playwright/test";
import { installAdminApi } from "./api-fixtures";

test.use({ serviceWorkers: "allow" });

test("a new service worker activates and reloads an already controlled client", async ({ page }) => {
  await installAdminApi(page);
  await page.goto("/boxes");
  await page.evaluate(async () => {
    await navigator.serviceWorker.ready;
  });
  await page.reload();
  await expect.poll(() => page.evaluate(() => Boolean(navigator.serviceWorker.controller))).toBe(true);

  const serviceWorkerPath = resolve(process.cwd(), "dist/sw.js");
  const originalServiceWorker = await readFile(serviceWorkerPath, "utf8");
  const changedServiceWorker = `${originalServiceWorker}\n// playwright-update-${Date.now()}\n`;

  try {
    const previousTimeOrigin = await page.evaluate(() => performance.timeOrigin);
    await writeFile(serviceWorkerPath, changedServiceWorker);
    await page.evaluate(async () => {
      const registration = await navigator.serviceWorker.getRegistration();
      if (!registration) throw new Error("service worker registration is missing");
      await registration.update();
    }).catch((error: unknown) => {
      if (!(error instanceof Error) || !/execution context|navigation|destroyed/i.test(error.message)) throw error;
    });
    await expect.poll(async () => {
      try { return await page.evaluate(() => performance.timeOrigin); }
      catch { return previousTimeOrigin; }
    }, { timeout: 15_000 }).not.toBe(previousTimeOrigin);

    await expect(page.getByRole("heading", { name: "Sign in", exact: true })).toBeVisible();
    const navigationType = await page.evaluate(() => {
      const entry = performance.getEntriesByType("navigation").at(-1) as PerformanceNavigationTiming | undefined;
      return entry?.type;
    });
    expect(navigationType).toBe("reload");
  } finally {
    await writeFile(serviceWorkerPath, originalServiceWorker);
  }
});
