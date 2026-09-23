import type { Page, Route } from "@playwright/test";
import type { AccessControlEntry, Agent, Box, Host, Member, Meta, Team, Workspace } from "../src/api/types";

const NOW = "2026-01-15T12:00:00.000Z";

export const adminMeta: Meta = {
  serverVersion: "e2e",
  apiVersion: "v1",
  enabledRuntimes: ["omp", "codex", "claude"],
  runtimeModels: { omp: ["default"], codex: ["gpt-5.3-codex"], claude: ["claude-sonnet-4-6"] },
  currentUser: {
    id: "user-admin",
    login: "admin",
    displayName: "Admin User",
    role: "admin",
    status: "active",
    mustChangePassword: false
  }
};

export const agents: Agent[] = [
  {
    id: "agent-primary",
    name: "Primary Agent",
    runtimeType: "omp",
    version: 3,
    model: "default",
    systemPrompt: "Work carefully.",
    createdAt: NOW,
    updatedAt: NOW
  },
  {
    id: "agent-claude",
    name: "Claude Agent",
    runtimeType: "claude",
    version: 1,
    model: "claude-sonnet-4-6",
    systemPrompt: "",
    createdAt: NOW,
    updatedAt: NOW
  }
];

export const hosts: Host[] = [
  {
    id: "host-online",
    name: "Online Host",
    status: "online",
    systemHostname: "online.example.test",
    os: "darwin",
    arch: "arm64",
    daemonVersion: "0.1.0",
    runtimes: ["omp", "codex", "claude"],
    lastSeenAt: NOW,
    maxActiveBoxes: 4
  },
  {
    id: "host-offline",
    name: "Offline Host",
    status: "offline",
    systemHostname: "offline.example.test",
    os: "linux",
    arch: "amd64",
    daemonVersion: "0.1.0",
    runtimes: ["omp"],
    lastSeenAt: NOW,
    maxActiveBoxes: 2
  }
];

export const workspaces: Workspace[] = [
  {
    id: "workspace-project",
    hostId: "host-online",
    name: "Project Workspace",
    path: "/srv/project",
    status: "ready",
    createdAt: NOW
  }
];

export const boxes: Box[] = [
  {
    id: "box-owned",
    name: "Owned Box",
    agentId: "agent-primary",
    hostId: "host-online",
    workspaceId: "workspace-project",
    visibility: "private",
    status: "running",
    ownerUserId: "user-admin",
    runtimeType: "omp",
    runtimeSessionMode: "new",
    version: 1,
    createdAt: NOW,
    updatedAt: NOW
  },
  {
    id: "box-second",
    name: "Second Box",
    agentId: "agent-primary",
    hostId: "host-online",
    workspaceId: "workspace-project",
    visibility: "private",
    status: "idle",
    ownerUserId: "user-admin",
    runtimeType: "omp",
    runtimeSessionMode: "new",
    version: 1,
    createdAt: NOW,
    updatedAt: NOW
  },
  {
    id: "box-developer",
    name: "Developer Box",
    agentId: "agent-primary",
    hostId: "host-online",
    workspaceId: "workspace-project",
    visibility: "private",
    status: "idle",
    ownerUserId: "user-developer",
    runtimeType: "omp",
    runtimeSessionMode: "new",
    version: 1,
    createdAt: NOW,
    updatedAt: NOW
  }
];

const members: Member[] = [
  {
    id: "user-admin",
    organizationId: "org-e2e",
    login: "admin",
    displayName: "Admin User",
    role: "admin",
    status: "active",
    hasPassword: true,
    mustChangePassword: false,
    createdAt: NOW
  },
  {
    id: "user-developer",
    organizationId: "org-e2e",
    login: "developer",
    displayName: "Developer User",
    role: "user",
    status: "active",
    hasPassword: true,
    mustChangePassword: false,
    createdAt: NOW
  }
];

const teams: Team[] = [
  {
    id: "team-platform",
    organizationId: "org-e2e",
    slug: "platform",
    name: "Platform Team",
    description: "Platform engineers",
    createdAt: NOW,
    updatedAt: NOW
  }
];

const workspaceAcl: AccessControlEntry[] = [
  {
    id: "acl-owner",
    organizationId: "org-e2e",
    resourceId: "workspace-project",
    userId: "user-admin",
    role: "owner",
    createdByUserId: "user-admin",
    createdAt: NOW
  }
];

interface ApiFixtureOptions {
  deleteFailures?: Record<string, string>;
  meta?: Meta;
  hostResponses?: Host[][];
}

export interface ApiObservations {
  agentCreates: Array<Record<string, unknown>>;
  boxCreates: Array<Record<string, unknown>>;
  workspaceCreates: Array<Record<string, unknown>>;
  deletes: string[];
  hostCascadeDeletes: string[];
}

export function installUserApi(page: Page): Promise<ApiObservations> {
  return installAdminApi(page, {
    meta: {
      ...adminMeta,
      currentUser: {
        ...adminMeta.currentUser,
        id: "user-developer",
        login: "developer",
        displayName: "Developer User",
        role: "user"
      }
    }
  });
}
export async function installAdminApi(page: Page, options: ApiFixtureOptions = {}): Promise<ApiObservations> {
  const observations: ApiObservations = {
    agentCreates: [],
    boxCreates: [],
    workspaceCreates: [],
    deletes: [],
    hostCascadeDeletes: []
  };
  const meta = options.meta ?? adminMeta;
  let createdBox: Box | undefined;
  let hostReadIndex = 0;

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const method = request.method();
    const requestURL = new URL(request.url());
    const path = requestURL.pathname.replace(/^\/api\/v1/, "");
    if (method === "GET" && path === "/boxes") return json(route, meta.currentUser.role === "admin" ? boxes : boxes.filter((box) => box.ownerUserId === meta.currentUser.id || box.visibility === "org"));
    if (method === "GET" && path === "/meta") return json(route, meta);
    if (method === "GET" && path === "/notifications") return json(route, []);
    if (method === "GET" && path === "/agents") return json(route, agents);
    if (method === "GET" && path === "/hosts") {
      const values = options.hostResponses?.[Math.min(hostReadIndex, options.hostResponses.length - 1)] ?? hosts;
      hostReadIndex++;
      return json(route, values);
    }
    if (method === "GET" && path === "/workspaces") return json(route, workspaces);
    if (method === "GET" && path === "/boxes") return json(route, boxes);
    if (method === "GET" && path === "/members") return json(route, members);
    if (method === "GET" && path === "/workspaces/workspace-project/acl") return json(route, workspaceAcl);
    if (method === "GET" && path === "/boxes/box-owned") return json(route, { ...boxes[0], lastEventSeq: 0, activeRunId: null, run: null });
    if (method === "POST" && path === "/agents") {
      const body = request.postDataJSON() as Record<string, unknown>;
      observations.agentCreates.push(body);
      return json(route, { id: "agent-created", name: body.name, runtimeType: body.runtimeType, version: 1, model: body.model, systemPrompt: body.systemPrompt ?? "", createdAt: NOW, updatedAt: NOW }, 201);
    }
    if (method === "GET" && path === "/workspaces/workspace-project") return json(route, workspaces[0]);
    if (method === "GET" && createdBox && path === `/boxes/${createdBox.id}`) {
      return json(route, { ...createdBox, lastEventSeq: 0, activeRunId: null, run: null });
    }

    if (method === "POST" && path === "/workspaces") {
      const body = request.postDataJSON() as Record<string, unknown>;
      observations.workspaceCreates.push(body);
      return json(route, {
        id: "workspace-created",
        hostId: body.hostId,
        name: body.name,
        path: body.path,
        status: "ready",
        createdAt: NOW
      }, 201);
    }
    if (method === "POST" && path === "/boxes") {
      const body = request.postDataJSON() as Record<string, unknown>;
      const selectedAgent = agents.find((agent) => agent.id === body.agentId);
      observations.boxCreates.push(body);
      createdBox = {
        id: "box-created",
        name: String(body.name),
        agentId: String(body.agentId),
        hostId: String(body.hostId),
        workspaceId: String(body.workspaceId),
        visibility: "private",
        status: "created",
        runtimeSessionMode: body.runtimeSessionMode ?? "new",
        runtimeSessionRef: body.runtimeSessionRef,
        ownerUserId: meta.currentUser.id,
        runtimeType: selectedAgent?.runtimeType ?? "omp",
        createdAt: NOW,
        updatedAt: NOW
      };
      return json(route, createdBox, 201);
    }

    if (method === "DELETE") {
      observations.deletes.push(path);
      if (path.startsWith("/hosts/") && requestURL.searchParams.get("cascade") === "true") observations.hostCascadeDeletes.push(path);
      const failure = options.deleteFailures?.[path];
      if (failure) return json(route, { message: failure }, 409);
      return json(route, {});
    }

    return json(route, { message: `Unhandled E2E API request: ${method} ${path}` }, 404);
  });

  return observations;
}

function json(route: Route, body: unknown, status = 200) {
  return route.fulfill({
    status,
    contentType: "application/json",
    headers: { "Cache-Control": "no-store" },
    body: JSON.stringify(body)
  });
}
