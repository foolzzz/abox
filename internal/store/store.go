package store

import (
	"context"
	"time"

	"agentbox/internal/domain"
)

type Store interface {
	Close()
	Migrate(ctx context.Context) error
	EnsureDevelopmentTenant(ctx context.Context, login string) (domain.User, error)
	ListMembers(ctx context.Context, user domain.User) ([]domain.Member, error)
	UpdateMemberRole(ctx context.Context, user domain.User, memberID, role string) (domain.Member, error)
	ListTeams(ctx context.Context, user domain.User) ([]domain.Team, error)

	ListAgents(ctx context.Context, user domain.User) ([]domain.Agent, error)
	CreateAgent(ctx context.Context, user domain.User, input domain.CreateAgentInput) (domain.Agent, error)
	GetAgent(ctx context.Context, user domain.User, id string) (domain.Agent, error)

	ListHosts(ctx context.Context, user domain.User) ([]domain.Host, error)
	UpsertHost(ctx context.Context, host domain.Host, daemonInstanceID string, lastAck uint64) (domain.Host, error)
	TouchHost(ctx context.Context, hostID, daemonInstanceID string, lastAck uint64) error
	GetHost(ctx context.Context, user domain.User, id string) (domain.Host, error)
	GetHostForOrganization(ctx context.Context, organizationID, id string) (domain.Host, error)

	ListWorkspaces(ctx context.Context, user domain.User) ([]domain.Workspace, error)
	CreateWorkspace(ctx context.Context, user domain.User, input domain.CreateWorkspaceInput) (domain.Workspace, *domain.HostCommand, error)
	GetWorkspace(ctx context.Context, user domain.User, id string) (domain.Workspace, error)
	GetWorkspaceForOrganization(ctx context.Context, organizationID, id string) (domain.Workspace, error)
	ListWorkspaceACL(ctx context.Context, user domain.User, workspaceID string) ([]domain.ResourceACL, error)
	ReplaceWorkspaceACL(ctx context.Context, user domain.User, workspaceID string, entries []domain.ResourceACLEntryInput) ([]domain.ResourceACL, error)

	ListBoxes(ctx context.Context, user domain.User) ([]domain.Box, error)
	CreateBox(ctx context.Context, user domain.User, input domain.CreateBoxInput) (domain.Box, error)
	DeleteBox(ctx context.Context, user domain.User, boxID string) (*domain.HostCommand, error)
	GetBox(ctx context.Context, user domain.User, id string) (domain.Box, error)
	GetBoxForHost(ctx context.Context, hostID, boxID string) (domain.Box, error)
	SetBoxStatus(ctx context.Context, boxID string, from []domain.BoxStatus, to domain.BoxStatus) error
	ListBoxACL(ctx context.Context, user domain.User, boxID string) ([]domain.ResourceACL, error)
	ReplaceBoxACL(ctx context.Context, user domain.User, boxID string, entries []domain.ResourceACLEntryInput) ([]domain.ResourceACL, error)
	AuthorizeBoxOperation(ctx context.Context, user domain.User, boxID string) error

	ListMessages(ctx context.Context, user domain.User, boxID string, limit int) ([]domain.Message, error)
	SendMessage(ctx context.Context, user domain.User, boxID string, input domain.SendMessageInput) (domain.Message, *domain.Run, *domain.HostCommand, error)
	ClaimNextRun(ctx context.Context, boxID string) (*domain.Run, *domain.HostCommand, error)
	SetMessageApplied(ctx context.Context, messageID, runID string) error
	CancelQueuedMessage(ctx context.Context, user domain.User, boxID, messageID string) (domain.Message, error)

	ListEvents(ctx context.Context, user domain.User, boxID string, afterSeq int64, limit int) ([]domain.BoxEvent, error)
	AppendEvents(ctx context.Context, events []domain.BoxEvent) ([]domain.BoxEvent, error)
	ApplyRuntimeEvent(ctx context.Context, event domain.BoxEvent) error

	CreateHostCommand(ctx context.Context, user domain.User, command domain.HostCommand) (domain.HostCommand, error)
	PendingHostCommands(ctx context.Context, hostID string, limit int) ([]domain.HostCommand, error)
	UpdateHostCommand(ctx context.Context, commandID, status, errorCode, errorMessage string, result []byte) error

	ListApprovals(ctx context.Context, user domain.User) ([]domain.Approval, error)
	ResolveApproval(ctx context.Context, user domain.User, approvalID, decision string) (domain.Approval, *domain.HostCommand, error)
	ExpireApprovals(ctx context.Context, limit int) ([]domain.HostCommand, error)
	HibernateIdleBoxes(ctx context.Context, limit int) ([]domain.HostCommand, error)

	ListSchedules(ctx context.Context, user domain.User) ([]domain.Schedule, error)
	CreateSchedule(ctx context.Context, user domain.User, input domain.CreateScheduleInput) (domain.Schedule, error)
	UpdateSchedule(ctx context.Context, user domain.User, scheduleID string, input domain.UpdateScheduleInput) (domain.Schedule, error)
	DeleteSchedule(ctx context.Context, user domain.User, scheduleID string) error
	ListScheduleExecutions(ctx context.Context, user domain.User, scheduleID string, limit int) ([]domain.ScheduleExecution, error)
	TriggerScheduleWebhook(ctx context.Context, scheduleID, idempotencyKey, bodySHA256 string) (domain.ScheduleExecution, []domain.HostCommand, error)
	ProcessDueSchedules(ctx context.Context, limit int) ([]domain.HostCommand, error)
	DispatchAutomationRuns(ctx context.Context, limit int) ([]domain.HostCommand, error)

	ListNotifications(ctx context.Context, user domain.User, limit int) ([]domain.Notification, error)
	MarkNotificationRead(ctx context.Context, user domain.User, notificationID string) error
	MarkAllNotificationsRead(ctx context.Context, user domain.User) error
	PruneOperationalData(ctx context.Context, operationalBefore, auditBefore time.Time, limit int) (int64, error)

	ListSubagents(ctx context.Context, user domain.User, boxID string) ([]domain.SubagentInstance, error)
	ListTodos(ctx context.Context, user domain.User, boxID string) ([]domain.TodoItem, error)
	ListArtifacts(ctx context.Context, user domain.User, boxID string) ([]domain.Artifact, error)
	GetArtifactForDownload(ctx context.Context, user domain.User, boxID, artifactID string) (domain.Artifact, error)
	RequestWorkspaceDiff(ctx context.Context, user domain.User, boxID, baseRef, headRef string) (domain.WorkspaceDiff, *domain.HostCommand, error)
}
