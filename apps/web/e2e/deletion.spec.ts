import { expect, test } from "@playwright/test";
import { hosts, installAdminApi, installUserApi } from "./api-fixtures";

test("Box delete control stays visible and surfaces backend conflicts in its modal", async ({ page }) => {
  const api = await installAdminApi(page, {
    deleteFailures: { "/boxes/box-owned": "The Box still has active work." }
  });

  await page.goto("/boxes");

  const deleteBox = page.getByRole("button", { name: "Delete Owned Box", exact: true });
  await expect(deleteBox).toBeVisible();
  await deleteBox.click();

  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Delete Owned Box?", exact: true })).toBeVisible();
  await dialog.getByRole("button", { name: "Delete session", exact: true }).click();
  await expect(dialog.getByRole("alert")).toHaveText("The Box still has active work.");
  expect(api.deletes).toEqual(["/boxes/box-owned"]);
});

test("multiple Box sessions can be selected and deleted together", async ({ page }) => {
  const api = await installAdminApi(page);
  await page.goto("/boxes");

  await page.getByRole("checkbox", { name: "Select Owned Box", exact: true }).check();
  await page.getByRole("checkbox", { name: "Select Second Box", exact: true }).check();
  await page.getByRole("button", { name: "Delete selected (2)", exact: true }).click();

  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Delete 2 sessions?", exact: true })).toBeVisible();
  await expect(dialog.getByText("Owned Box", { exact: true })).toBeVisible();
  await expect(dialog.getByText("Second Box", { exact: true })).toBeVisible();
  await dialog.getByRole("button", { name: "Delete 2 sessions", exact: true }).click();

  await expect(page.getByRole("heading", { name: "Owned Box", exact: true })).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Second Box", exact: true })).toHaveCount(0);
  expect(api.deletes).toEqual(["/boxes/box-owned", "/boxes/box-second"]);
});

test("bulk Box deletion keeps failed sessions selected and reports each error", async ({ page }) => {
  const api = await installAdminApi(page, { deleteFailures: { "/boxes/box-second": "The session is protected." } });
  await page.goto("/boxes");

  await page.getByRole("checkbox", { name: "Select all visible boxes", exact: true }).check();
  await page.getByRole("button", { name: "Delete selected (2)", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "Delete 2 sessions", exact: true }).click();

  await expect(page.getByRole("heading", { name: "Owned Box", exact: true })).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Second Box", exact: true })).toBeVisible();
  await expect(dialog.getByRole("alert").first()).toHaveText("Second Box: The session is protected.");
  await expect(page.getByRole("checkbox", { name: "Select Second Box", exact: true })).toBeChecked();
  expect(api.deletes).toEqual(["/boxes/box-owned", "/boxes/box-second"]);
});

test("Host delete controls expose the offline prerequisite and allow offline deletion", async ({ page }) => {
  const api = await installAdminApi(page);

  await page.goto("/hosts");

  const deleteOnlineHost = page.getByRole("button", { name: "Delete Online Host", exact: true });
  const deleteOfflineHost = page.getByRole("button", { name: "Delete Offline Host", exact: true });
  await expect(deleteOnlineHost).toBeVisible();
  await expect(deleteOfflineHost).toBeVisible();

  await deleteOnlineHost.click();
  let dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Delete Online Host?", exact: true })).toBeVisible();
  await expect(dialog.getByRole("status").getByText(/must be offline before it can be deleted/i)).toBeVisible();
  await expect(dialog.getByRole("button", { name: "Delete host", exact: true })).toBeDisabled();
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();

  await deleteOfflineHost.click();
  dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Delete Offline Host?", exact: true })).toBeVisible();
  const confirmDelete = dialog.getByRole("button", { name: "Delete host", exact: true });
  await expect(confirmDelete).toBeEnabled();
  await confirmDelete.click();

  await expect(deleteOfflineHost).toHaveCount(0);
  expect(api.deletes).toEqual(["/hosts/host-offline"]);
});

test("offline Host deletion can cascade dependent resources", async ({ page }) => {
  const api = await installAdminApi(page);
  await page.goto("/hosts");
  await page.getByRole("button", { name: "Delete Offline Host", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("checkbox", { name: /Also terminate dependent Boxes/ }).check();
  await dialog.getByRole("button", { name: "Delete host and dependencies", exact: true }).click();
  expect(api.hostCascadeDeletes).toEqual(["/hosts/host-offline"]);
});

test("Agent delete control stays on the card and opens a working confirmation modal", async ({ page }) => {
  const api = await installAdminApi(page);

  await page.goto("/agents");

  const deleteAgent = page.getByRole("button", { name: "Delete Primary Agent", exact: true });
  await expect(deleteAgent).toBeVisible();
  await deleteAgent.click();

  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Delete Primary Agent?", exact: true })).toBeVisible();
  await dialog.getByRole("button", { name: "Delete Agent", exact: true }).click();

  await expect(deleteAgent).toHaveCount(0);
  expect(api.deletes).toEqual(["/agents/agent-primary"]);
});

test("Workspace row exposes sharing and deletion controls with their modals", async ({ page }) => {
  const api = await installAdminApi(page);

  await page.goto("/workspaces");

  const openSharing = page.getByRole("button", { name: "Open sharing for Project Workspace", exact: true });
  const deleteWorkspace = page.getByRole("button", { name: "Delete Project Workspace", exact: true });
  await expect(openSharing).toBeVisible();
  await expect(deleteWorkspace).toBeVisible();

  await openSharing.click();
  let dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Share Project Workspace", exact: true })).toBeVisible();
  await expect(dialog.getByRole("list", { name: "Access rules", exact: true })).toBeVisible();
  await expect(dialog.getByRole("button", { name: "Save sharing", exact: true })).toBeVisible();
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();

  await deleteWorkspace.click();
  dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Delete Project Workspace?", exact: true })).toBeVisible();
  await dialog.getByRole("button", { name: "Delete workspace", exact: true }).click();

  await expect(deleteWorkspace).toHaveCount(0);
  expect(api.deletes).toEqual(["/workspaces/workspace-project"]);
});

test("ordinary Box owners can manage only their own filtered Box", async ({ page }) => {
  await installUserApi(page);

  await page.goto("/boxes");
  await expect(page.getByRole("heading", { name: "Developer Box", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Delete Developer Box", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Owned Box", exact: true })).toHaveCount(0);

  await page.goto("/hosts");
  await expect(page.getByRole("button", { name: /^Delete / })).toHaveCount(0);

  await page.goto("/agents");
  await expect(page.getByRole("button", { name: "Delete Primary Agent", exact: true })).toHaveCount(0);

  await page.goto("/workspaces");
  await expect(page.getByRole("button", { name: "Delete Project Workspace", exact: true })).toHaveCount(0);
});

test("Box list defaults to the current owner and can filter other users", async ({ page }) => {
  await installAdminApi(page);
  await page.goto("/boxes");
  const owner = page.getByRole("combobox", { name: "Filter by owner", exact: true });
  await expect(owner).toHaveValue("me");
  await expect(page.getByRole("heading", { name: "Owned Box", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Developer Box", exact: true })).toHaveCount(0);
  await owner.selectOption("user-developer");
  await expect(page.getByRole("heading", { name: "Developer Box", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Owned Box", exact: true })).toHaveCount(0);
  await owner.selectOption("all");
  await expect(page.getByRole("heading", { name: "Owned Box", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Developer Box", exact: true })).toBeVisible();
});

test("Agent Box creation auto-registers an unregistered path in one submit", async ({ page }) => {
  const api = await installAdminApi(page);

  await page.goto("/boxes?create=1");
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Create an Agent Box", exact: true })).toBeVisible();
  await expect(dialog.getByRole("group", { name: "Project directory", exact: true })).toBeVisible();
  await expect(dialog.getByText("Creation progress", { exact: true })).toHaveCount(0);
  await expect(dialog.getByRole("button", { name: "Choose project directory", exact: true })).toHaveCount(0);
  await dialog.getByRole("combobox").first().selectOption("agent-primary");

  await dialog.getByLabel("Box name").fill("One Step Box");
  await dialog.getByLabel("Local project directory").fill("/srv/one-step");
  await dialog.getByRole("button", { name: "Create box", exact: true }).click();

  await expect(page).toHaveURL(/\/boxes\/box-created\/agent-terminal$/);
  expect(api.workspaceCreates).toEqual([{ hostId: "host-online", name: "one-step", path: "/srv/one-step" }]);
  expect(api.boxCreates).toEqual([{ name: "One Step Box", agentId: "agent-primary", hostId: "host-online", workspaceId: "workspace-created", runtimeSessionMode: "new" }]);
});

test("duplicate Claude session references can be resumed by multiple Boxes", async ({ page }) => {
  const api = await installAdminApi(page);
  const sessionRef = "3f8f5ddb-c916-421b-8e21-4423a0a96af6";
  for (const name of ["Resume Claude A", "Resume Claude B"]) {
    await page.goto("/boxes?create=1");
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("combobox").first().selectOption("agent-claude");
    await dialog.getByRole("radio", { name: "Resume an existing session", exact: true }).check();
    await dialog.getByLabel("Claude session ID", { exact: true }).fill(sessionRef);
    await dialog.getByLabel("Box name").fill(name);
    await dialog.getByLabel("Local project directory").fill(`/srv/${name.endsWith("A") ? "resume-a" : "resume-b"}`);
    await dialog.getByRole("button", { name: "Create box", exact: true }).click();
    await expect(page).toHaveURL(/\/boxes\/box-created\/agent-terminal$/);
  }
  expect(api.boxCreates.map((request) => ({ mode: request.runtimeSessionMode, ref: request.runtimeSessionRef }))).toEqual([
    { mode: "resume", ref: sessionRef },
    { mode: "resume", ref: sessionRef }
  ]);
});

test("Agent creation hides System Prompt and prefills Runtime model defaults", async ({ page }) => {
  const api = await installAdminApi(page);
  await page.goto("/agents?create=1");
  const dialog = page.getByRole("dialog");
  const model = dialog.locator('input[list^="agent-model-suggestions-"]');
  await expect(model).toHaveValue("gpt-5.6-sol");
  await expect(dialog.getByRole("textbox", { name: /System Prompt/i })).toHaveCount(0);
  await dialog.getByRole("radio", { name: /^Claude / }).check();
  await expect(model).toHaveValue("claude-opus-4-6");
  await dialog.getByLabel("Name").fill("Default Claude Agent");
  await dialog.getByRole("button", { name: "New agent", exact: true }).click();
  expect(api.agentCreates).toEqual([{ name: "Default Claude Agent", runtimeType: "claude", model: "claude-opus-4-6", systemPrompt: "" }]);
});

test("Agent creation shows every Runtime advertised by an online Host as available", async ({ page }) => {
  await installAdminApi(page);
  await page.goto("/agents?create=1");

  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Define an agent", exact: true })).toBeVisible();
  await expect(dialog.getByText("1 online host(s) available", { exact: true })).toHaveCount(3);
  await expect(dialog.getByText("Unavailable", { exact: true })).toHaveCount(0);
});

test("open Agent creation refreshes stale Runtime availability", async ({ page }) => {
  const offlineHosts = hosts.map((host) => ({ ...host, status: "offline" as const }));
  await installAdminApi(page, { hostResponses: [offlineHosts, hosts] });
  await page.goto("/agents");
  await page.getByRole("button", { name: "New agent", exact: true }).click();

  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("1 online host(s) available", { exact: true })).toHaveCount(3);
  await expect(dialog.getByText("Unavailable", { exact: true })).toHaveCount(0);
});

test("Terminal widescreen fills the browser page and exits without Fullscreen API", async ({ page }) => {
  await installAdminApi(page);
  await page.goto("/boxes/box-owned/agent-terminal");

  const widescreen = page.getByRole("button", { name: "Widescreen", exact: true });
  await expect(widescreen).toBeVisible();
  await widescreen.click();
  await expect(page.getByRole("button", { name: "Exit widescreen", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Agent Terminal", exact: true })).toBeHidden();
  const dimensions = await page.locator(".terminal-page").evaluate((element) => {
    const rectangle = element.getBoundingClientRect();
    return { top: rectangle.top, left: rectangle.left, width: rectangle.width, height: rectangle.height, viewportWidth: innerWidth, viewportHeight: innerHeight };
  });
  expect(dimensions.top).toBe(0);
  expect(dimensions.left).toBe(0);
  expect(dimensions.width).toBe(dimensions.viewportWidth);
  expect(dimensions.height).toBe(dimensions.viewportHeight);
  expect(await page.evaluate(() => document.fullscreenElement)).toBeNull();

  await page.keyboard.press("Escape");
  await expect(page.getByRole("button", { name: "Widescreen", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Agent Terminal", exact: true })).toBeVisible();
});
