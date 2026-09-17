package domain

import (
	"encoding/json"
	"time"
)

type User struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organizationId"`
	Login          string    `json:"login"`
	DisplayName    string    `json:"displayName"`
	Role           string    `json:"role"`
	CreatedAt      time.Time `json:"createdAt"`
}

type Member struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organizationId"`
	Login          string    `json:"login"`
	DisplayName    string    `json:"displayName"`
	Role           string    `json:"role"`
	CreatedAt      time.Time `json:"createdAt"`
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
	ID             string    `json:"id"`
	OrganizationID string    `json:"organizationId"`
	Name           string    `json:"name"`
	AgentID        string    `json:"agentId"`
	AgentVersionID string    `json:"agentVersionId"`
	HostID         string    `json:"hostId"`
	WorkspaceID    string    `json:"workspaceId"`
	OwnerUserID    string    `json:"ownerUserId"`
	RuntimeType    string    `json:"runtimeType"`
	Status         BoxStatus `json:"status"`
	Version        int64     `json:"version"`
	LastEventSeq   int64     `json:"lastEventSeq"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
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

type Message struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organizationId"`
	BoxID          string          `json:"boxId"`
	RunID          string          `json:"runId,omitempty"`
	BoxSeq         int64           `json:"boxSeq"`
	AuthorType     string          `json:"authorType"`
	AuthorUserID   string          `json:"authorUserId,omitempty"`
	AuthorName     string          `json:"authorName,omitempty"`
	Role           string          `json:"role"`
	Delivery       Delivery        `json:"delivery,omitempty"`
	Status         string          `json:"status"`
	Content        json.RawMessage `json:"content"`
	PlainText      string          `json:"plainText,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
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
	ID             string          `json:"id"`
	OrganizationID string          `json:"organizationId"`
	BoxID          string          `json:"boxId"`
	RunID          string          `json:"runId"`
	ToolName       string          `json:"toolName"`
	RiskLevel      string          `json:"riskLevel"`
	Status         string          `json:"status"`
	Payload        json.RawMessage `json:"payload"`
	ExpiresAt      time.Time       `json:"expiresAt"`
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
	Name        string
	AgentID     string
	HostID      string
	WorkspaceID string
}

type SendMessageInput struct {
	Content        string
	Delivery       Delivery
	IdempotencyKey string
}
