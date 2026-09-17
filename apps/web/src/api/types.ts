export type RuntimeType = "omp" | "claude" | "acp";
export type HostStatus = "enrolling" | "online" | "draining" | "offline" | "revoked";
export type WorkspaceStatus = "provisioning" | "ready" | "error" | "archived";
export type BoxStatus =
  | "created"
  | "starting"
  | "idle"
  | "running"
  | "waiting_approval"
  | "hibernating"
  | "hibernated"
  | "error"
  | "terminated";
export type RunStatus =
  | "queued"
  | "dispatching"
  | "running"
  | "waiting_approval"
  | "interrupting"
  | "disconnected"
  | "succeeded"
  | "failed"
  | "aborted"
  | "lost"
  | "cancelled";
export type DeliveryMode = "prompt" | "steer" | "follow_up";
export type ApprovalStatus = "pending" | "approved" | "denied" | "expired" | "cancelled";
export type RiskLevel = "low" | "medium" | "high" | "critical";

export interface Meta {
  serverVersion: string;
  apiVersion: string;
  minDaemonVersion?: string;
}

export interface Agent {
  id: string;
  name: string;
  slug?: string;
  runtimeType: RuntimeType;
  version: number;
  model?: string | null;
  systemPrompt: string;
  createdAt?: string;
  updatedAt?: string;
}

export interface CreateAgentRequest {
  name: string;
  runtimeType: RuntimeType;
  model?: string | null;
  systemPrompt: string;
}

export interface Host {
  id: string;
  name: string;
  slug?: string;
  status: HostStatus;
  os?: string;
  arch?: string;
  daemonVersion?: string;
  runtimes: string[];
  lastSeenAt?: string | null;
  maxActiveBoxes?: number;
}

export interface Workspace {
  id: string;
  hostId: string;
  name: string;
  path: string;
  kind?: string;
  status: WorkspaceStatus;
  createdAt?: string;
}

export interface CreateWorkspaceRequest {
  hostId: string;
  name: string;
  path: string;
}

export interface Box {
  id: string;
  name: string;
  agentId: string;
  hostId: string;
  workspaceId: string;
  status: BoxStatus;
  runtimeType?: RuntimeType | string;
  version?: number;
  lastEventSeq?: number;
  createdAt?: string;
  updatedAt?: string;
}

export interface CreateBoxRequest {
  name: string;
  agentId: string;
  hostId: string;
  workspaceId: string;
}

export interface BoxSnapshot extends Box {
  activeRunId?: string | null;
  lastEventSeq: number;
  run?: Run | null;
}

export interface Run {
  id: string;
  boxId: string;
  runtimeInstanceId?: string;
  triggerMessageId: string;
  status: RunStatus;
  queuedAt: string;
  startedAt?: string | null;
  finishedAt?: string | null;
  terminalReason?: string;
  errorMessage?: string;
}

export interface Message {
  id: string;
  boxId: string;
  runId?: string | null;
  boxSeq?: number;
  authorType?: string;
  authorName?: string | null;
  role: "user" | "assistant" | "system";
  delivery?: DeliveryMode | null;
  status: string;
  content: unknown;
  plainText?: string;
  createdAt: string;
}

export interface SendMessageRequest {
  content: string;
  delivery: DeliveryMode;
}

export interface Approval {
  id: string;
  boxId: string;
  runId: string;
  toolName: string;
  riskLevel: RiskLevel;
  status: ApprovalStatus;
  payload?: Record<string, unknown>;
  expiresAt: string;
}

export interface ApprovalDecisionRequest {
  decision: "approved" | "denied";
}

export type AgentEventType =
  | "runtime.starting"
  | "runtime.ready"
  | "runtime.exited"
  | "run.started"
  | "run.completed"
  | "run.failed"
  | "message.started"
  | "message.delta"
  | "message.completed"
  | "tool.started"
  | "tool.progress"
  | "tool.completed"
  | "tool.failed"
  | "subagent.started"
  | "subagent.progress"
  | "subagent.completed"
  | "todo.updated"
  | "approval.requested"
  | "approval.resolved"
  | "artifact.created"
  | "notice"
  | (string & {});

export interface EventActor {
  kind: "user" | "main_agent" | "subagent" | "system" | "daemon" | string;
  id?: string | null;
}

export interface AgentEvent<TPayload extends Record<string, unknown> = Record<string, unknown>> {
  eventId: string;
  boxId: string;
  runId?: string | null;
  runtimeInstanceId?: string | null;
  seq: number;
  type: AgentEventType;
  occurredAt: string;
  actor?: EventActor;
  actorKind?: string;
  actorId?: string;
  payload: TPayload;
}

export interface ApiErrorBody {
  code?: string;
  message?: string;
  error?: string | { code?: string; message?: string };
  details?: unknown;
}
