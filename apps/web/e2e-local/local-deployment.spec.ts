import { expect, test } from "@playwright/test";

const username = process.env.AGENTBOX_E2E_USERNAME;
const password = process.env.AGENTBOX_E2E_PASSWORD;

test.beforeEach(async ({ page }) => {
  if (!username || !password) throw new Error("AGENTBOX_E2E_USERNAME and AGENTBOX_E2E_PASSWORD are required");
  await page.goto("/");
  if (await page.getByRole("heading", { name: "Sign in", exact: true }).isVisible()) {
    await page.getByLabel("Username").fill(username);
    await page.getByLabel("Password").fill(password);
    await page.getByRole("button", { name: "Sign in", exact: true }).click();
  }
  await expect(page.getByRole("complementary", { name: "Primary navigation" })).toBeVisible();
});

test("deletion controls are visible on every managed resource surface", async ({ page }) => {
  await page.goto("/boxes");
  const boxDeletes = page.getByRole("button", { name: /^Delete .+/ });
  await expect(boxDeletes.first()).toBeVisible();
  await boxDeletes.first().click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await page.getByRole("dialog").getByRole("button", { name: "Cancel", exact: true }).click();

  await page.goto("/hosts");
  const hostDeletes = page.getByRole("button", { name: /^Delete .+/ });
  await expect(hostDeletes.first()).toBeVisible();
  await hostDeletes.first().click();
  const hostDialog = page.getByRole("dialog");
  await expect(hostDialog).toBeVisible();
  const hostConfirm = hostDialog.getByRole("button", { name: "Delete host", exact: true });
  if (await hostDialog.getByText(/must be offline before it can be deleted/i).count()) {
    await expect(hostConfirm).toBeDisabled();
  }
  await hostDialog.getByRole("button", { name: "Cancel", exact: true }).click();

  await page.goto("/agents");
  const agentDeletes = page.getByRole("button", { name: /^Delete .+/ });
  await expect(agentDeletes.first()).toBeVisible();
  await agentDeletes.first().click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await page.getByRole("dialog").getByRole("button", { name: "Cancel", exact: true }).click();

  await page.goto("/workspaces");
  const workspaceDeletes = page.getByRole("button", { name: /^Delete .+/ });
  await expect(workspaceDeletes.first()).toBeVisible();
  await workspaceDeletes.first().click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await page.getByRole("dialog").getByRole("button", { name: "Cancel", exact: true }).click();
});

test("Agent Box creation is a single live form", async ({ page }) => {
  await page.goto("/boxes?create=1");
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Create an Agent Box", exact: true })).toBeVisible();
  await expect(dialog.getByLabel("Box name")).toBeVisible();
  await expect(dialog.getByRole("combobox")).toHaveCount(2);
  await expect(dialog.getByRole("combobox").first()).toBeVisible();
  await expect(dialog.getByRole("group", { name: "Project directory", exact: true })).toBeVisible();
  await expect(dialog.getByRole("button", { name: "Create box", exact: true })).toBeVisible();
  await expect(dialog.getByRole("button", { name: "Choose project directory", exact: true })).toHaveCount(0);
});
