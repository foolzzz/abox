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
  await expect.poll(async () => page.evaluate(async () => {
    const response = await fetch("/api/v1/hosts");
    if (!response.ok) return 0;
    const hosts = await response.json() as Array<{ status: string; runtimes: string[] }>;
    return hosts.filter((host) => host.status === "online" && host.runtimes.length > 0).length;
  }), { timeout: 30_000 }).toBeGreaterThan(0);
});

test("deletion controls are visible on every managed resource surface", async ({ page }) => {
  const liveSetup = await page.evaluate(async () => {
    const read = async (path: string) => {
      const response = await fetch(`/api/v1${path}`);
      if (!response.ok) throw new Error(`GET ${path} failed: ${response.status}`);
      return response.json();
    };
    const [agents, hosts, workspaces] = await Promise.all([read("/agents"), read("/hosts"), read("/workspaces")]);
    const host = hosts.find((candidate: { status: string }) => candidate.status === "online");
    const workspace = workspaces.find((candidate: { hostId: string; status: string }) => candidate.hostId === host?.id && candidate.status === "ready");
    if (!host || !workspace) throw new Error("live E2E requires an online Host and ready Workspace");
    const suffix = Date.now();
    let agent = agents.find((candidate: { runtimeType: string }) => host.runtimes.includes(candidate.runtimeType));
    let createdAgentId: string | undefined;
    if (!agent) {
      const response = await fetch("/api/v1/agents", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name: `Bulk E2E Agent ${suffix}`, runtimeType: host.runtimes[0], model: null, systemPrompt: "" }) });
      if (!response.ok) throw new Error(`create live Agent failed: ${response.status}`);
      agent = await response.json();
      createdAgentId = agent.id;
    }
    const boxes = await Promise.all(["A", "B"].map(async (part) => {
      const response = await fetch("/api/v1/boxes", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name: `Bulk E2E ${suffix} ${part}`, agentId: agent.id, hostId: host.id, workspaceId: workspace.id }) });
      if (!response.ok) throw new Error(`create live Box failed: ${response.status}`);
      return response.json() as Promise<{ id: string; name: string }>;
    }));
    return { boxes, createdAgentId };
  });

  const liveBoxes = liveSetup.boxes;
  await page.goto(`/boxes/${liveBoxes[0].id}/agent-terminal`);
  await page.getByRole("button", { name: "Widescreen", exact: true }).click();
  const wideMetrics = await page.locator(".terminal-page").evaluate((element) => {
    const rectangle = element.getBoundingClientRect();
    return { top: rectangle.top, left: rectangle.left, width: rectangle.width, height: rectangle.height, viewportWidth: innerWidth, viewportHeight: innerHeight };
  });
  expect(wideMetrics).toEqual({ top: 0, left: 0, width: wideMetrics.viewportWidth, height: wideMetrics.viewportHeight, viewportWidth: wideMetrics.viewportWidth, viewportHeight: wideMetrics.viewportHeight });
  await page.keyboard.press("Escape");
  await expect(page.getByRole("button", { name: "Widescreen", exact: true })).toBeVisible();

  await page.goto("/boxes");

  for (const box of liveBoxes) {
    await expect(page.getByRole("button", { name: `Delete ${box.name}`, exact: true })).toBeVisible();
    await page.getByRole("checkbox", { name: `Select ${box.name}`, exact: true }).check();
  }
  await page.getByRole("button", { name: "Delete selected (2)", exact: true }).click();
  const bulkDialog = page.getByRole("dialog");
  await expect(bulkDialog.getByRole("heading", { name: "Delete 2 sessions?", exact: true })).toBeVisible();
  await bulkDialog.getByRole("button", { name: "Delete 2 sessions", exact: true }).click();
  for (const box of liveBoxes) await expect(page.getByRole("heading", { name: box.name, exact: true })).toHaveCount(0);

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
  if (liveSetup.createdAgentId) {
    await page.evaluate(async (agentId) => {
      const response = await fetch(`/api/v1/agents/${agentId}`, { method: "DELETE" });
      if (!response.ok) throw new Error(`delete live Agent failed: ${response.status}`);
    }, liveSetup.createdAgentId);
  }
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

test("live Agent creation reports OMP, Codex, and Claude as available", async ({ page }) => {
  await page.goto("/agents?create=1");
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Define an agent", exact: true })).toBeVisible();
  await expect(dialog.getByText("1 online host(s) available", { exact: true })).toHaveCount(3);
  await expect(dialog.getByText("Unavailable", { exact: true })).toHaveCount(0);
});

test("live API allows duplicate Claude resume references across Boxes", async ({ page }) => {
  const sessionRef = "3f8f5ddb-c916-421b-8e21-4423a0a96af6";
  const result = await page.evaluate(async (resumeRef) => {
    const request = async (path: string, init?: RequestInit) => {
      const response = await fetch(`/api/v1${path}`, init);
      if (!response.ok) throw new Error(`${init?.method ?? "GET"} ${path} failed: ${response.status}`);
      return response.status === 204 ? undefined : response.json();
    };
    const [hosts, workspaces] = await Promise.all([request("/hosts"), request("/workspaces")]);
    const host = hosts.find((candidate: { status: string; runtimes: string[] }) => candidate.status === "online" && candidate.runtimes.includes("claude"));
    const workspace = workspaces.find((candidate: { hostId: string; status: string }) => candidate.hostId === host?.id && candidate.status === "ready");
    if (!host || !workspace) throw new Error("live resume E2E requires an online Claude Host and ready Workspace");
    const suffix = Date.now();
    const agent = await request("/agents", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name: `Resume E2E Agent ${suffix}`, runtimeType: "claude", model: "claude-opus-4-6", systemPrompt: "" }) });
    const boxes: Array<{ id: string; runtimeSessionMode: string; runtimeSessionRef: string }> = [];
    try {
      for (const part of ["A", "B"]) {
        const box = await request("/boxes", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name: `Resume E2E ${suffix} ${part}`, agentId: agent.id, hostId: host.id, workspaceId: workspace.id, runtimeSessionMode: "resume", runtimeSessionRef: resumeRef }) });
        boxes.push(box);
      }
      return boxes.map((box) => ({ mode: box.runtimeSessionMode, ref: box.runtimeSessionRef }));
    } finally {
      for (const box of boxes) await request(`/boxes/${box.id}`, { method: "DELETE" });
      await request(`/agents/${agent.id}`, { method: "DELETE" });
    }
  }, sessionRef);
  expect(result).toEqual([{ mode: "resume", ref: sessionRef }, { mode: "resume", ref: sessionRef }]);
});
