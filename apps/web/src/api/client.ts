import { isObjectRecord } from "../lib/data";
import type {
  AccessControlEntry,
  Agent,
  Approval,
  ApprovalDecisionRequest,
  Box,
  BoxSnapshot,
  CreateAgentRequest,
  CreateBoxRequest,
  CreateWorkspaceRequest,
  Host,
  Member,
  Message,
  Meta,
  ReplaceAccessControlRequest,
  SendMessageRequest,
  Team,
  UpdateMemberRoleRequest,
  Workspace
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
  getMeta: (signal?: AbortSignal) => request<Meta>("/meta", { signal }),
  listMembers: (signal?: AbortSignal) => request<Member[]>("/members", { signal }),
  updateMemberRole: (memberId: string, body: UpdateMemberRoleRequest, signal?: AbortSignal) =>
    request<Member>(`/members/${encode(memberId)}/role`, { method: "PATCH", body, signal }),
  listTeams: (signal?: AbortSignal) => request<Team[]>("/teams", { signal }),
  listAgents: (signal?: AbortSignal) => request<Agent[]>("/agents", { signal }),
  createAgent: (body: CreateAgentRequest, signal?: AbortSignal) =>
    request<Agent>("/agents", { method: "POST", body, signal }),
  listHosts: (signal?: AbortSignal) => request<Host[]>("/hosts", { signal }),
  listWorkspaces: (signal?: AbortSignal) => request<Workspace[]>("/workspaces", { signal }),
  createWorkspace: (body: CreateWorkspaceRequest, signal?: AbortSignal) =>
    request<Workspace>("/workspaces", { method: "POST", body, signal }),
  getWorkspaceAcl: (workspaceId: string, signal?: AbortSignal) =>
    request<AccessControlEntry[]>(`/workspaces/${encode(workspaceId)}/acl`, { signal }),
  replaceWorkspaceAcl: (workspaceId: string, body: ReplaceAccessControlRequest, signal?: AbortSignal) =>
    request<AccessControlEntry[]>(`/workspaces/${encode(workspaceId)}/acl`, { method: "PUT", body, signal }),
  listBoxes: (signal?: AbortSignal) => request<Box[]>("/boxes", { signal }),
  createBox: (body: CreateBoxRequest, signal?: AbortSignal) =>
    request<Box>("/boxes", { method: "POST", body, signal }),
  getBox: (boxId: string, signal?: AbortSignal) =>
    request<BoxSnapshot>(`/boxes/${encode(boxId)}`, { signal }),
  listMessages: (boxId: string, signal?: AbortSignal) =>
    request<Message[]>(`/boxes/${encode(boxId)}/messages`, { signal }),
  sendMessage: (boxId: string, body: SendMessageRequest, idempotencyKey: string, signal?: AbortSignal) =>
    request<Message>(`/boxes/${encode(boxId)}/messages`, {
      method: "POST",
      body,
      signal,
      headers: { "Idempotency-Key": idempotencyKey }
    }),
  cancelMessage: (boxId: string, messageId: string, signal?: AbortSignal) =>
    request<Message>(`/boxes/${encode(boxId)}/messages/${encode(messageId)}`, { method: "DELETE", signal }),
  getBoxAcl: (boxId: string, signal?: AbortSignal) =>
    request<AccessControlEntry[]>(`/boxes/${encode(boxId)}/acl`, { signal }),
  replaceBoxAcl: (boxId: string, body: ReplaceAccessControlRequest, signal?: AbortSignal) =>
    request<AccessControlEntry[]>(`/boxes/${encode(boxId)}/acl`, { method: "PUT", body, signal }),
  interruptBox: (boxId: string, signal?: AbortSignal) =>
    request<void>(`/boxes/${encode(boxId)}/interrupt`, { method: "POST", signal }),
  stopBox: (boxId: string, signal?: AbortSignal) =>
    request<void>(`/boxes/${encode(boxId)}/stop`, { method: "POST", signal }),
  resumeBox: (boxId: string, signal?: AbortSignal) =>
    request<void>(`/boxes/${encode(boxId)}/resume`, { method: "POST", signal }),
  listApprovals: (signal?: AbortSignal) => request<Approval[]>("/approvals", { signal }),
  decideApproval: (approvalId: string, body: ApprovalDecisionRequest, signal?: AbortSignal) =>
    request<Approval>(`/approvals/${encode(approvalId)}/decision`, { method: "POST", body, signal })
};

export function errorMessage(error: unknown): string {
  if (error instanceof ApiError) return error.message;
  if (error instanceof Error) return error.message;
  return "The request could not be completed.";
}
