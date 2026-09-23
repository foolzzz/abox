package domain

import (
	"encoding/json"
	"time"
)

type User struct {
	ID                 string    `json:"id"`
	OrganizationID     string    `json:"organizationId"`
	Login              string    `json:"login"`
	DisplayName        string    `json:"displayName"`
	Role               string    `json:"role"`
	Status             string    `json:"status"`
	MustChangePassword bool      `json:"mustChangePassword"`
	CreatedAt          time.Time `json:"createdAt"`
}

type Member struct {
	ID                 string    `json:"id"`
	OrganizationID     string    `json:"organizationId"`
	Login              string    `json:"login"`
	DisplayName        string    `json:"displayName"`
	Role               string    `json:"role"`
	Status             string    `json:"status"`
	HasPassword        bool      `json:"hasPassword"`
	MustChangePassword bool      `json:"mustChangePassword"`
	CreatedAt          time.Time `json:"createdAt"`
}

type AccountCredentials struct {
	User         User
	PasswordHash string
}

type CreateAccountInput struct {
	Username     string
	DisplayName  string
	Role         string
	PasswordHash string
}

type UpdateAccountInput struct {
	DisplayName *string
	Role        *string
	Status      *string
}

type Team struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organizationId"`
	Slug           string    `json:"slug"`
	Name           string    `json:"name"`
	Description    string    `json:"description,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type ResourceACL struct {
	ID              string    `json:"id"`
	OrganizationID  string    `json:"organizationId"`
	ResourceID      string    `json:"resourceId"`
	UserID          string    `json:"userId,omitempty"`
	TeamID          string    `json:"teamId,omitempty"`
	Role            string    `json:"role"`
	CreatedByUserID string    `json:"createdByUserId"`
	CreatedAt       time.Time `json:"createdAt"`
}

type ResourceACLEntryInput struct {
	UserID string
	TeamID string
	Role   string
}

type Agent struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organizationId"`
	Name           string          `json:"name"`
	Slug           string          `json:"slug"`
	Version        int             `json:"version"`
	RuntimeType    string          `json:"runtimeType"`
	Model          string          `json:"model,omitempty"`
	SystemPrompt   string          `json:"systemPrompt"`
	ToolPolicy     json.RawMessage `json:"toolPolicy,omitempty"`
	SkillPolicy    json.RawMessage `json:"skillPolicy,omitempty"`
	ApprovalPolicy json.RawMessage `json:"approvalPolicy,omitempty"`
	RuntimeConfig  json.RawMessage `json:"runtimeConfig,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
}

type Host struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organizationId"`
	Name           string          `json:"name"`
	Slug           string          `json:"slug"`
	Status         HostStatus      `json:"status"`
	SystemHostname string          `json:"systemHostname,omitempty"`
	OS             string          `json:"os,omitempty"`
	Arch           string          `json:"arch,omitempty"`
	DaemonVersion  string          `json:"daemonVersion,omitempty"`
	Runtimes       []string        `json:"runtimes"`
	Labels         json.RawMessage `json:"labels,omitempty"`
	LastSeenAt     *time.Time      `json:"lastSeenAt,omitempty"`
	MaxActiveBoxes int             `json:"maxActiveBoxes"`
}

type Workspace struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organizationId"`
	HostID         string    `json:"hostId"`
	Name           string    `json:"name"`
	Path           string    `json:"path"`
	Kind           string    `json:"kind"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"createdAt"`
}

type Box struct {
	ID                 string    `json:"id"`
	OrganizationID     string    `json:"organizationId"`
	Name               string    `json:"name"`
	AgentID            string    `json:"agentId"`
	AgentVersionID     string    `json:"agentVersionId"`
	HostID             string    `json:"hostId"`
	WorkspaceID        string    `json:"workspaceId"`
	OwnerUserID        string    `json:"ownerUserId"`
	RuntimeType        string    `json:"runtimeType"`
	Model              string    `json:"model,omitempty"`
	RuntimeSessionMode string    `json:"runtimeSessionMode"`
	RuntimeSessionRef  string    `json:"runtimeSessionRef,omitempty"`
	Visibility         string    `json:"visibility"`
	Status             BoxStatus `json:"status"`
	Version            int64     `json:"version"`
	LastEventSeq       int64     `json:"lastEventSeq"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

type RuntimeSessionAttachment struct {
	ID          string     `json:"id"`
	BoxID       string     `json:"boxId"`
	BoxName     string     `json:"boxName"`
	HostID      string     `json:"hostId"`
	RuntimeType string     `json:"runtimeType"`
	SessionRef  string     `json:"sessionRef"`
	AttachMode  string     `json:"attachMode"`
	InputPolicy string     `json:"inputPolicy"`
	Status      string     `json:"status"`
	AttachedAt  time.Time  `json:"attachedAt"`
	DetachedAt  *time.Time `json:"detachedAt,omitempty"`
}

type Run struct {
	ID                string     `json:"id"`
	OrganizationID    string     `json:"organizationId"`
	BoxID             string     `json:"boxId"`
	RuntimeInstanceID string     `json:"runtimeInstanceId,omitempty"`
	TriggerMessageID  string     `json:"triggerMessageId"`
	Status            RunStatus  `json:"status"`
	QueuedAt          time.Time  `json:"queuedAt"`
	StartedAt         *time.Time `json:"startedAt,omitempty"`
	FinishedAt        *time.Time `json:"finishedAt,omitempty"`
	TerminalReason    string     `json:"terminalReason,omitempty"`
	ErrorMessage      string     `json:"errorMessage,omitempty"`
}

type PresentationContext struct {
	ViewportWidth        int    `json:"viewportWidth,omitempty"`
	ViewportHeight       int    `json:"viewportHeight,omitempty"`
	DeviceClass          string `json:"deviceClass,omitempty"`
	Orientation          string `json:"orientation,omitempty"`
	Touch                bool   `json:"touch"`
	Locale               string `json:"locale,omitempty"`
	Timezone             string `json:"timezone,omitempty"`
	PrefersReducedMotion bool   `json:"prefersReducedMotion"`
	Surface              string `json:"surface,omitempty"`
}

type Message struct {
	ID                  string          `json:"id"`
	OrganizationID      string          `json:"organizationId"`
	BoxID               string          `json:"boxId"`
	RunID               string          `json:"runId,omitempty"`
	BoxSeq              int64           `json:"boxSeq"`
	AuthorType          string          `json:"authorType"`
	AuthorUserID        string          `json:"authorUserId,omitempty"`
	AuthorName          string          `json:"authorName,omitempty"`
	Role                string          `json:"role"`
	Delivery            Delivery        `json:"delivery,omitempty"`
	Status              string          `json:"status"`
	Content             json.RawMessage `json:"content"`
	PlainText           string          `json:"plainText,omitempty"`
	PresentationContext json.RawMessage `json:"presentationContext,omitempty"`
	CreatedAt           time.Time       `json:"createdAt"`
}

type BoxEvent struct {
	OrganizationID    string          `json:"organizationId"`
	BoxID             string          `json:"boxId"`
	Seq               int64           `json:"seq"`
	EventID           string          `json:"eventId"`
	HostID            string          `json:"hostId,omitempty"`
	DaemonEventID     string          `json:"daemonEventId,omitempty"`
	RunID             string          `json:"runId,omitempty"`
	RuntimeInstanceID string          `json:"runtimeInstanceId,omitempty"`
	EventType         string          `json:"type"`
	ActorKind         string          `json:"actorKind"`
	ActorID           string          `json:"actorId,omitempty"`
	Payload           json.RawMessage `json:"payload"`
	OccurredAt        time.Time       `json:"occurredAt"`
	IngestedAt        time.Time       `json:"ingestedAt"`
}

type HostCommand struct {
	ID                string          `json:"id"`
	OrganizationID    string          `json:"organizationId"`
	HostID            string          `json:"hostId"`
	BoxID             string          `json:"boxId,omitempty"`
	RunID             string          `json:"runId,omitempty"`
	RuntimeInstanceID string          `json:"runtimeInstanceId,omitempty"`
	CommandType       string          `json:"commandType"`
	Payload           json.RawMessage `json:"payload"`
	IdempotencyKey    string          `json:"idempotencyKey"`
	Status            string          `json:"status"`
	CreatedAt         time.Time       `json:"createdAt"`
}

type Approval struct {
	ID               string          `json:"id"`
	OrganizationID   string          `json:"organizationId"`
	BoxID            string          `json:"boxId"`
	RunID            string          `json:"runId"`
	ToolName         string          `json:"toolName"`
	RiskLevel        string          `json:"riskLevel"`
	Status           string          `json:"status"`
	Payload          json.RawMessage `json:"payload"`
	DecisionPayload  json.RawMessage `json:"decisionPayload,omitempty"`
	RequestedAt      time.Time       `json:"requestedAt"`
	ExpiresAt        time.Time       `json:"expiresAt"`
	ResolvedAt       *time.Time      `json:"resolvedAt,omitempty"`
	ResolvedByUserID string          `json:"resolvedByUserId,omitempty"`
}

type CreateAgentInput struct {
	Name         string
	RuntimeType  string
	Model        string
	SystemPrompt string
}

type CreateWorkspaceInput struct {
	HostID string
	Name   string
	Path   string
	Kind   string
}

type CreateBoxInput struct {
	Name               string
	AgentID            string
	Model              string
	HostID             string
	WorkspaceID        string
	RuntimeSessionMode string
	RuntimeSessionRef  string
}

type SendMessageInput struct {
	Content             string
	Delivery            Delivery
	PresentationContext PresentationContext
	IdempotencyKey      string
}

type Schedule struct {
	ID                string     `json:"id"`
	OrganizationID    string     `json:"organizationId"`
	Name              string     `json:"name"`
	AgentID           string     `json:"agentId"`
	AgentVersionID    string     `json:"agentVersionId,omitempty"`
	HostID            string     `json:"hostId"`
	WorkspaceID       string     `json:"workspaceId"`
	CronExpression    string     `json:"cronExpression"`
	Timezone          string     `json:"timezone"`
	PromptTemplate    string     `json:"promptTemplate"`
	ConcurrencyPolicy string     `json:"concurrencyPolicy"`
	Status            string     `json:"status"`
	NextRunAt         *time.Time `json:"nextRunAt,omitempty"`
	LastRunAt         *time.Time `json:"lastRunAt,omitempty"`
	CreatedByUserID   string     `json:"createdByUserId,omitempty"`
	Version           int64      `json:"version"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

type CreateScheduleInput struct {
	Name              string
	AgentID           string
	HostID            string
	WorkspaceID       string
	CronExpression    string
	Timezone          string
	PromptTemplate    string
	ConcurrencyPolicy string
	Status            string
}

type UpdateScheduleInput struct {
	Name              *string
	AgentID           *string
	HostID            *string
	WorkspaceID       *string
	CronExpression    *string
	Timezone          *string
	PromptTemplate    *string
	ConcurrencyPolicy *string
	Status            *string
}

type ScheduleExecution struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organizationId"`
	ScheduleID     string     `json:"scheduleId"`
	ScheduledFor   time.Time  `json:"scheduledFor"`
	BoxID          string     `json:"boxId,omitempty"`
	RunID          string     `json:"runId,omitempty"`
	Status         string     `json:"status"`
	Reason         string     `json:"reason,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	FinishedAt     *time.Time `json:"finishedAt,omitempty"`
}

type Notification struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organizationId"`
	UserID         string     `json:"userId,omitempty"`
	Type           string     `json:"type"`
	Title          string     `json:"title"`
	Body           string     `json:"body"`
	Status         string     `json:"status"`
	BoxID          string     `json:"boxId,omitempty"`
	RunID          string     `json:"runId,omitempty"`
	ScheduleID     string     `json:"scheduleId,omitempty"`
	ApprovalID     string     `json:"approvalId,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	ReadAt         *time.Time `json:"readAt,omitempty"`
}

type SubagentInstance struct {
	ID                    string          `json:"id"`
	BoxID                 string          `json:"boxId"`
	RunID                 string          `json:"runId"`
	RuntimeInstanceID     string          `json:"runtimeInstanceId"`
	ExternalAgentID       string          `json:"externalAgentId"`
	ParentExternalAgentID string          `json:"parentExternalAgentId,omitempty"`
	AgentType             string          `json:"agentType,omitempty"`
	Label                 string          `json:"label,omitempty"`
	Status                string          `json:"status"`
	SessionRef            string          `json:"sessionRef,omitempty"`
	Metadata              json.RawMessage `json:"metadata"`
	StartedAt             time.Time       `json:"startedAt"`
	FinishedAt            *time.Time      `json:"finishedAt,omitempty"`
}

type TodoItem struct {
	ID                string     `json:"id"`
	BoxID             string     `json:"boxId"`
	RunID             string     `json:"runId,omitempty"`
	RuntimeInstanceID string     `json:"runtimeInstanceId"`
	ExternalTodoID    string     `json:"externalTodoId"`
	PhaseName         string     `json:"phaseName,omitempty"`
	Content           string     `json:"content"`
	Position          int        `json:"position"`
	Status            string     `json:"status"`
	BlockReason       string     `json:"blockReason,omitempty"`
	UpdatedAt         time.Time  `json:"updatedAt"`
	CompletedAt       *time.Time `json:"completedAt,omitempty"`
}

type Artifact struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organizationId,omitempty"`
	BoxID          string          `json:"boxId"`
	RunID          string          `json:"runId,omitempty"`
	HostID         string          `json:"hostId,omitempty"`
	Kind           string          `json:"kind"`
	Name           string          `json:"name"`
	MIMEType       string          `json:"mimeType,omitempty"`
	SizeBytes      int64           `json:"sizeBytes"`
	SHA256         string          `json:"sha256"`
	Status         string          `json:"status"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
	ExpiresAt      *time.Time      `json:"expiresAt,omitempty"`
	DownloadURL    string          `json:"downloadUrl"`
	HostPath       string          `json:"-"`
	WorkspacePath  string          `json:"-"`
}

type WorkspaceDiffFile struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	OldPath   string `json:"oldPath,omitempty"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch,omitempty"`
}

type WorkspaceDiff struct {
	RequestID   string              `json:"requestId,omitempty"`
	Status      string              `json:"status,omitempty"`
	BaseRef     string              `json:"baseRef,omitempty"`
	HeadRef     string              `json:"headRef,omitempty"`
	GeneratedAt *time.Time          `json:"generatedAt,omitempty"`
	Files       []WorkspaceDiffFile `json:"files,omitempty"`
	Error       string              `json:"error,omitempty"`
}
