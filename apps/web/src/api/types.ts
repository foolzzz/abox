export type RuntimeType = "omp" | "codex" | "claude" | "acp";
export type HostStatus = "enrolling" | "online" | "draining" | "offline" | "revoked";
export type OrganizationRole = "admin" | "user";
export type ResourceRole = "owner" | "operator" | "viewer";
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

export interface CurrentUser {
  id: string;
  login: string;
  displayName: string;
  role: OrganizationRole;
  status: "active" | "disabled";
  mustChangePassword: boolean;
}

export interface Meta {
  serverVersion: string;
  apiVersion: string;
  minDaemonVersion?: string;
  enabledRuntimes: RuntimeType[];
  runtimeModels: Partial<Record<RuntimeType, string[]>>;
  currentUser: CurrentUser;
}

export interface Member {
  id: string;
  organizationId: string;
  login: string;
  displayName: string;
  role: OrganizationRole;
  status: "active" | "disabled";
  hasPassword: boolean;
  mustChangePassword: boolean;
  createdAt: string;
}

export interface LoginRequest { username: string; password: string; }
export interface ChangePasswordRequest { currentPassword: string; newPassword: string; }
export interface CreateAccountRequest { username: string; displayName: string; role: OrganizationRole; password: string; }
export interface UpdateAccountRequest { displayName?: string; role?: OrganizationRole; status?: "active" | "disabled"; }
export interface ResetPasswordRequest { password: string; }

export interface Team {
  id: string;
  organizationId: string;
  slug: string;
  name: string;
  description?: string;
  createdAt: string;
  updatedAt: string;
}

export interface AccessControlEntry {
  id: string;
  organizationId: string;
  resourceId: string;
  userId?: string;
  teamId?: string;
  role: ResourceRole;
  createdByUserId: string;
  createdAt: string;
}

export interface AccessControlInput {
  userId?: string;
  teamId?: string;
  role: ResourceRole;
}

export interface ReplaceAccessControlRequest {
  entries: AccessControlInput[];
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
  systemHostname?: string;
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
  model?: string;
  visibility: "private" | "org";
  status: BoxStatus;
  ownerUserId?: string;
  runtimeType?: RuntimeType | string;
  runtimeSessionMode?: "new" | "resume";
  runtimeSessionRef?: string;
  version?: number;
  lastEventSeq?: number;
  createdAt?: string;
  updatedAt?: string;
}

export interface CreateBoxRequest {
  name: string;
  agentId: string;
  model?: string;
  hostId: string;
  workspaceId: string;
  runtimeSessionMode?: "new" | "resume";
  runtimeSessionRef?: string;
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



export type ScheduleStatus = "active" | "paused" | "deleted";
export type ConcurrencyPolicy = "skip" | "queue" | "replace";

export interface Schedule {
  id: string;
  name: string;
  agentId: string;
  hostId: string;
  workspaceId: string;
  cronExpression: string;
  timezone: string;
  promptTemplate: string;
  concurrencyPolicy: ConcurrencyPolicy;
  status: ScheduleStatus;
  nextRunAt?: string | null;
  lastRunAt?: string | null;
  createdByUserId?: string;
  createdAt: string;
  updatedAt: string;
}

export interface CreateScheduleRequest {
  name: string;
  agentId: string;
  hostId: string;
  workspaceId: string;
  cronExpression: string;
  timezone: string;
  promptTemplate: string;
  concurrencyPolicy: ConcurrencyPolicy;
  status: Exclude<ScheduleStatus, "deleted">;
}

export type UpdateScheduleRequest = Partial<CreateScheduleRequest>;

export interface ScheduleExecution {
  id: string;
  scheduleId: string;
  scheduledFor: string;
  boxId?: string | null;
  runId?: string | null;
  status: "claimed" | "skipped" | "dispatched" | "completed" | "failed";
  reason?: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface Notification {
  id: string;
  type: string;
  title: string;
  body: string;
  status: "unread" | "read";
  boxId?: string | null;
  runId?: string | null;
  scheduleId?: string | null;
  createdAt: string;
  readAt?: string | null;
}

export interface SubagentInstance {
  id: string;
  boxId: string;
  runId: string;
  runtimeInstanceId: string;
  externalAgentId: string;
  parentExternalAgentId?: string | null;
  agentType?: string | null;
  label?: string | null;
  status: "running" | "idle" | "completed" | "failed" | "cancelled" | "parked";
  sessionRef?: string | null;
  metadata?: Record<string, unknown>;
  startedAt: string;
  finishedAt?: string | null;
}

export interface TodoItem {
  id: string;
  boxId: string;
  runId?: string | null;
  runtimeInstanceId: string;
  externalTodoId: string;
  phaseName?: string | null;
  content: string;
  position: number;
  status: "pending" | "in_progress" | "completed" | "blocked" | "abandoned";
  blockReason?: string | null;
  updatedAt: string;
}

export interface Artifact {
  id: string;
  boxId: string;
  runId?: string | null;
  kind: "file" | "image" | "report" | "archive" | "log" | "other";
  name: string;
  mimeType?: string | null;
  sizeBytes: number;
  sha256: string;
  status: "ready" | "deleted";
  metadata?: Record<string, unknown>;
  createdAt: string;
  expiresAt?: string | null;
  downloadUrl?: string;
}

export interface WorkspaceDiffFile {
  path: string;
  oldPath?: string | null;
  status: "added" | "modified" | "deleted" | "renamed" | "untracked";
  additions: number;
  deletions: number;
  patch?: string | null;
}

export interface WorkspaceDiff {
  baseRef?: string | null;
  headRef?: string | null;
  generatedAt: string;
  files: WorkspaceDiffFile[];
}

export interface PendingWorkspaceDiff {
  status: "pending";
  requestId: string;
}



export interface ApiErrorBody {
  code?: string;
  message?: string;
  error?: string | { code?: string; message?: string };
  details?: unknown;
}
