import { isObjectRecord } from "../lib/data";
import type {
  AccessControlEntry,
  Agent,
  Artifact,
  Box,
  BoxSnapshot,
  CreateAgentRequest,
  CreateBoxRequest,
  CreateScheduleRequest,
  ChangePasswordRequest,
  CreateAccountRequest,
  CurrentUser,
  CreateWorkspaceRequest,
  Host,
  Member,
  Meta,
  LoginRequest,
  Notification,
  PendingWorkspaceDiff,
  ReplaceAccessControlRequest,
  ResetPasswordRequest,
  RuntimeSession,
  Schedule,
  ScheduleExecution,
  SubagentInstance,
  Team,
  TodoItem,
  UpdateAccountRequest,
  UpdateScheduleRequest,
  Workspace,
  WorkspaceDiff
} from "./types";

const API_BASE = "/api/v1";

export class ApiError extends Error {
  readonly status: number;
  readonly code?: string;
  readonly details?: unknown;

  constructor(message: string, status: number, code?: string, details?: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

type RequestOptions = Omit<RequestInit, "body"> & { body?: unknown };

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const headers = new Headers(options.headers);
  headers.set("Accept", "application/json");
  if (options.body !== undefined) headers.set("Content-Type", "application/json");

  const response = await fetch(`${API_BASE}${path}`, {
    ...options,
    headers,
    credentials: "same-origin",
    body: options.body === undefined ? undefined : JSON.stringify(options.body)
  });

  if (!response.ok) {
    const body = await readBody(response);
    const nested = isObjectRecord(body?.error) ? body.error : undefined;
    const message =
      (typeof nested?.message === "string" && nested.message) ||
      (typeof body?.message === "string" && body.message) ||
      (typeof body?.error === "string" && body.error) ||
      `${response.status} ${response.statusText}`;
    const code =
      (typeof nested?.code === "string" && nested.code) ||
      (typeof body?.code === "string" && body.code) ||
      undefined;
    throw new ApiError(message, response.status, code, body?.details);
  }

  if (response.status === 204 || response.headers.get("content-length") === "0") {
    return undefined as T;
  }

  const text = await response.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

async function download(path: string, signal?: AbortSignal): Promise<Blob> {
  const response = await fetch(`${API_BASE}${path}`, { credentials: "same-origin", signal });
  if (!response.ok) {
    const body = await readBody(response);
    const nested = isObjectRecord(body?.error) ? body.error : undefined;
    const message =
      (typeof nested?.message === "string" && nested.message) ||
      (typeof body?.message === "string" && body.message) ||
      (typeof body?.error === "string" && body.error) ||
      `${response.status} ${response.statusText}`;
    const code = (typeof nested?.code === "string" && nested.code) || (typeof body?.code === "string" && body.code) || undefined;
    throw new ApiError(message, response.status, code, body?.details);
  }
  return response.blob();
}

async function readBody(response: Response): Promise<Record<string, unknown> | undefined> {
  try {
    const value: unknown = await response.json();
    return isObjectRecord(value) ? value : undefined;
  } catch {
    return undefined;
  }
}


function encode(segment: string): string {
  return encodeURIComponent(segment);
}

export const api = {
  login: (body: LoginRequest, signal?: AbortSignal) =>
    request<CurrentUser>("/auth/login", { method: "POST", body, signal }),
  logout: (signal?: AbortSignal) => request<void>("/auth/logout", { method: "POST", signal }),
  changePassword: (body: ChangePasswordRequest, signal?: AbortSignal) =>
    request<CurrentUser>("/auth/change-password", { method: "POST", body, signal }),
  getMeta: (signal?: AbortSignal) => request<Meta>("/meta", { signal }),
  listMembers: (signal?: AbortSignal) => request<Member[]>("/members", { signal }),
  createAccount: (body: CreateAccountRequest, signal?: AbortSignal) =>
    request<Member>("/members", { method: "POST", body, signal }),
  updateAccount: (memberId: string, body: UpdateAccountRequest, signal?: AbortSignal) =>
    request<Member>(`/members/${encode(memberId)}`, { method: "PATCH", body, signal }),
  resetAccountPassword: (memberId: string, body: ResetPasswordRequest, signal?: AbortSignal) =>
    request<Member>(`/members/${encode(memberId)}/password`, { method: "PUT", body, signal }),
  deleteAccount: (memberId: string, signal?: AbortSignal) =>
    request<void>(`/members/${encode(memberId)}`, { method: "DELETE", signal }),
  listTeams: (signal?: AbortSignal) => request<Team[]>("/teams", { signal }),
  listAgents: (signal?: AbortSignal) => request<Agent[]>("/agents", { signal }),
  createAgent: (body: CreateAgentRequest, signal?: AbortSignal) =>
    request<Agent>("/agents", { method: "POST", body, signal }),
  deleteAgent: (agentId: string, signal?: AbortSignal) =>
    request<void>(`/agents/${encode(agentId)}`, { method: "DELETE", signal }),
  listHosts: (signal?: AbortSignal) => request<Host[]>("/hosts", { signal }),
  deleteHost: (hostId: string, cascade = false, signal?: AbortSignal) =>
    request<void>(`/hosts/${encode(hostId)}${cascade ? "?cascade=true" : ""}`, { method: "DELETE", signal }),
  listRuntimeSessions: (hostId: string, runtime = "claude", signal?: AbortSignal) =>
    request<RuntimeSession[]>(`/hosts/${encode(hostId)}/runtime-sessions?runtime=${encode(runtime)}`, { signal }),
  listWorkspaces: (signal?: AbortSignal) => request<Workspace[]>("/workspaces", { signal }),
  getWorkspace: (workspaceId: string, signal?: AbortSignal) =>
    request<Workspace>(`/workspaces/${encode(workspaceId)}`, { signal }),
  createWorkspace: (body: CreateWorkspaceRequest, signal?: AbortSignal) =>
    request<Workspace>("/workspaces", { method: "POST", body, signal }),
  deleteWorkspace: (workspaceId: string, signal?: AbortSignal) =>
    request<void>(`/workspaces/${encode(workspaceId)}`, { method: "DELETE", signal }),
  getWorkspaceAcl: (workspaceId: string, signal?: AbortSignal) =>
    request<AccessControlEntry[]>(`/workspaces/${encode(workspaceId)}/acl`, { signal }),
  replaceWorkspaceAcl: (workspaceId: string, body: ReplaceAccessControlRequest, signal?: AbortSignal) =>
    request<AccessControlEntry[]>(`/workspaces/${encode(workspaceId)}/acl`, { method: "PUT", body, signal }),
  listBoxes: (signal?: AbortSignal) => request<Box[]>("/boxes", { signal }),
  createBox: (body: CreateBoxRequest, signal?: AbortSignal) =>
    request<Box>("/boxes", { method: "POST", body, signal }),
  deleteBox: (boxId: string, signal?: AbortSignal) =>
    request<void>(`/boxes/${encode(boxId)}`, { method: "DELETE", signal }),
  getBox: (boxId: string, signal?: AbortSignal) =>
    request<BoxSnapshot>(`/boxes/${encode(boxId)}`, { signal }),
  updateBoxModel: (boxId: string, model: string, signal?: AbortSignal) =>
    request<BoxSnapshot>(`/boxes/${encode(boxId)}/model`, { method: "PATCH", body: { model }, signal }),
  updateBoxVisibility: (boxId: string, visibility: "private" | "org", signal?: AbortSignal) =>
    request<BoxSnapshot>(`/boxes/${encode(boxId)}/visibility`, { method: "PATCH", body: { visibility }, signal }),
  getBoxAcl: (boxId: string, signal?: AbortSignal) =>
    request<AccessControlEntry[]>(`/boxes/${encode(boxId)}/acl`, { signal }),
  replaceBoxAcl: (boxId: string, body: ReplaceAccessControlRequest, signal?: AbortSignal) =>
    request<AccessControlEntry[]>(`/boxes/${encode(boxId)}/acl`, { method: "PUT", body, signal }),
  listSchedules: (signal?: AbortSignal) => request<Schedule[]>("/schedules", { signal }),
  createSchedule: (body: CreateScheduleRequest, signal?: AbortSignal) =>
    request<Schedule>("/schedules", { method: "POST", body, signal }),
  updateSchedule: (scheduleId: string, body: UpdateScheduleRequest, signal?: AbortSignal) =>
    request<Schedule>(`/schedules/${encode(scheduleId)}`, { method: "PATCH", body, signal }),
  deleteSchedule: (scheduleId: string, signal?: AbortSignal) =>
    request<void>(`/schedules/${encode(scheduleId)}`, { method: "DELETE", signal }),
  listScheduleExecutions: (scheduleId: string, signal?: AbortSignal) =>
    request<ScheduleExecution[]>(`/schedules/${encode(scheduleId)}/executions`, { signal }),
  listNotifications: (signal?: AbortSignal) => request<Notification[]>("/notifications", { signal }),
  markNotificationRead: (notificationId: string, signal?: AbortSignal) =>
    request<Notification>(`/notifications/${encode(notificationId)}/read`, { method: "POST", signal }),
  markAllNotificationsRead: (signal?: AbortSignal) =>
    request<void>("/notifications/read-all", { method: "POST", signal }),
  listBoxSubagents: (boxId: string, signal?: AbortSignal) =>
    request<SubagentInstance[]>(`/boxes/${encode(boxId)}/subagents`, { signal }),
  listBoxTodos: (boxId: string, signal?: AbortSignal) =>
    request<TodoItem[]>(`/boxes/${encode(boxId)}/todos`, { signal }),
  listBoxArtifacts: (boxId: string, signal?: AbortSignal) =>
    request<Artifact[]>(`/boxes/${encode(boxId)}/artifacts`, { signal }),
  getBoxDiff: (boxId: string, signal?: AbortSignal) =>
    request<WorkspaceDiff | PendingWorkspaceDiff>(`/boxes/${encode(boxId)}/diff`, { signal }),
  downloadArtifact: (boxId: string, artifactId: string, signal?: AbortSignal) =>
    download(`/boxes/${encode(boxId)}/artifacts/${encode(artifactId)}/download`, signal)
};

export function errorMessage(error: unknown): string {
  if (error instanceof ApiError) return error.message;
  if (error instanceof Error) return error.message;
  return "The request could not be completed.";
}
