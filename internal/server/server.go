package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	hostv1 "agentbox/api"
	authpkg "agentbox/internal/auth"
	"agentbox/internal/domain"
	"agentbox/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"google.golang.org/grpc"
)

const (
	defaultAPIVersion       = "v1"
	defaultServerVersion    = "dev"
	defaultReplayLimit      = 1000
	defaultEventBuffer      = 256
	defaultHostCommandLimit = 500
)

type Options struct {
	Store                   store.Store
	SystemUser              domain.User
	EnrollmentToken         string
	ServerVersion           string
	PublicURL               string
	StaticDir               string
	Logger                  *slog.Logger
	ApprovalPollInterval    time.Duration
	HibernationPollInterval time.Duration
	ReaperBatchSize         int
	SchedulePollInterval    time.Duration
	RetentionPollInterval   time.Duration
	OperationalRetention    time.Duration
	AuditRetention          time.Duration
	WebhookSecret           string
	EnableCodex             bool
	EnableClaude            bool
	RuntimeModels           map[string][]string
	HeartbeatInterval       time.Duration
	HostPendingCommandLimit int
	SSEReplayLimit          int
	SSESubscriberBuffer     int
}

type Server struct {
	hostv1.UnimplementedHostServiceServer

	store             store.Store
	systemUser        domain.User
	enrollmentToken   string
	serverVersion     string
	staticRoot        *os.Root
	secureCookies     bool
	dummyPasswordHash string
	logger            *slog.Logger
	loginMu           sync.Mutex
	loginFailures     map[string]loginFailure

	approvalPollInterval    time.Duration
	hibernationPollInterval time.Duration
	reaperBatchSize         int
	schedulePollInterval    time.Duration
	retentionPollInterval   time.Duration
	operationalRetention    time.Duration
	auditRetention          time.Duration
	webhookSecret           string
	enableCodex             bool
	enableClaude            bool
	runtimeModels           map[string][]string
	heartbeatInterval       time.Duration
	hostCommandLimit        int
	replayLimit             int

	events          *eventHub
	hosts           *hostHub
	terminals       *terminalHub
	runtimeSessions *runtimeSessionHub
	metrics         *metrics
	router          http.Handler
}

func New(options Options) (*Server, error) {
	if options.Store == nil {
		return nil, errors.New("server store is required")
	}
	if options.SystemUser.ID == "" || options.SystemUser.OrganizationID == "" {
		return nil, errors.New("server system user is required")
	}
	if strings.TrimSpace(options.EnrollmentToken) == "" {
		return nil, errors.New("server enrollment token is required")
	}
	if options.ServerVersion == "" {
		options.ServerVersion = defaultServerVersion
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.ApprovalPollInterval <= 0 {
		options.ApprovalPollInterval = time.Second
	}
	if options.HibernationPollInterval <= 0 {
		options.HibernationPollInterval = 30 * time.Second
	}
	if options.ReaperBatchSize <= 0 {
		options.ReaperBatchSize = 100
	}
	if options.SchedulePollInterval <= 0 {
		options.SchedulePollInterval = time.Second
	}
	if options.RetentionPollInterval <= 0 {
		options.RetentionPollInterval = time.Hour
	}
	if options.OperationalRetention <= 0 {
		options.OperationalRetention = 30 * 24 * time.Hour
	}
	if options.AuditRetention <= 0 {
		options.AuditRetention = 365 * 24 * time.Hour
	}
	if options.HeartbeatInterval <= 0 {
		options.HeartbeatInterval = 15 * time.Second
	}
	if options.HostPendingCommandLimit <= 0 {
		options.HostPendingCommandLimit = defaultHostCommandLimit
	}
	if options.SSEReplayLimit <= 0 {
		options.SSEReplayLimit = defaultReplayLimit
	}
	if options.SSESubscriberBuffer <= 0 {
		options.SSESubscriberBuffer = defaultEventBuffer
	}
	runtimeModels := cloneRuntimeModels(options.RuntimeModels)
	if !options.EnableCodex {
		delete(runtimeModels, "codex")
	}
	if !options.EnableClaude {
		delete(runtimeModels, "claude")
	}
	if _, ok := runtimeModels["omp"]; !ok {
		runtimeModels["omp"] = []string{}
	}
	if options.EnableCodex {
		if _, ok := runtimeModels["codex"]; !ok {
			runtimeModels["codex"] = []string{}
		}
	}
	if options.EnableClaude {
		if _, ok := runtimeModels["claude"]; !ok {
			runtimeModels["claude"] = []string{}
		}
	}
	var staticRoot *os.Root
	if staticDir := strings.TrimSpace(options.StaticDir); staticDir != "" {
		var err error
		staticRoot, err = os.OpenRoot(staticDir)
		if err != nil {
			return nil, fmt.Errorf("open static directory: %w", err)
		}
	}
	dummyPasswordHash, err := authpkg.HashPassword("invalid-account-password")
	if err != nil {
		if staticRoot != nil {
			_ = staticRoot.Close()
		}
		return nil, fmt.Errorf("prepare password verification: %w", err)
	}

	metricSet := newMetrics()
	metricSet.codexEnabled = options.EnableCodex
	metricSet.claudeEnabled = options.EnableClaude
	server := &Server{
		store:                   options.Store,
		systemUser:              options.SystemUser,
		enrollmentToken:         options.EnrollmentToken,
		serverVersion:           options.ServerVersion,
		staticRoot:              staticRoot,
		secureCookies:           strings.HasPrefix(strings.ToLower(strings.TrimSpace(options.PublicURL)), "https://"),
		dummyPasswordHash:       dummyPasswordHash,
		logger:                  options.Logger,
		loginFailures:           make(map[string]loginFailure),
		approvalPollInterval:    options.ApprovalPollInterval,
		hibernationPollInterval: options.HibernationPollInterval,
		reaperBatchSize:         options.ReaperBatchSize,
		schedulePollInterval:    options.SchedulePollInterval,
		retentionPollInterval:   options.RetentionPollInterval,
		operationalRetention:    options.OperationalRetention,
		auditRetention:          options.AuditRetention,
		enableCodex:             options.EnableCodex,
		enableClaude:            options.EnableClaude,
		runtimeModels:           runtimeModels,
		webhookSecret:           strings.TrimSpace(options.WebhookSecret),
		heartbeatInterval:       options.HeartbeatInterval,
		hostCommandLimit:        options.HostPendingCommandLimit,
		replayLimit:             options.SSEReplayLimit,
		events:                  newEventHub(options.SSESubscriberBuffer, metricSet),
		hosts:                   newHostHub(metricSet),
		terminals:               newTerminalHub(),
		runtimeSessions:         newRuntimeSessionHub(),
		metrics:                 metricSet,
	}
	server.router = server.routes()
	return server, nil
}

func (s *Server) Handler() http.Handler {
	return s.router
}

func (s *Server) Close() error {
	if s.staticRoot == nil {
		return nil
	}
	return s.staticRoot.Close()
}

func (s *Server) RegisterGRPC(registrar grpc.ServiceRegistrar) {
	hostv1.RegisterHostServiceServer(registrar, s)
}

func (s *Server) Run(ctx context.Context) {
	go s.runApprovalReaper(ctx)
	go s.runHibernationReaper(ctx)
	go s.runAutomationScheduler(ctx)
	go s.runRetentionReaper(ctx)
	<-ctx.Done()
}

func (s *Server) routes() http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.Recoverer)
	router.Use(s.observeHTTP)

	router.Get("/healthz", s.handleHealth)
	router.Get("/readyz", s.handleReady)
	router.Get("/metrics", s.handleMetrics)
	router.With(s.authenticate, s.requirePasswordChanged, s.requireRole(roleUser)).Get("/diagnostics", s.handleDiagnostics)
	router.Post("/api/v1/auth/login", s.handleLogin)
	router.Post("/api/v1/webhooks/schedules/{scheduleID}", s.handleScheduleWebhook)
	router.With(s.authenticate).Get("/api/v1/meta", s.handleMeta)
	router.With(s.authenticate).Post("/api/v1/auth/logout", s.handleLogout)
	router.With(s.authenticate).Post("/api/v1/auth/change-password", s.handleChangePassword)

	router.Route("/api/v1", func(api chi.Router) {
		api.Use(s.authenticate)
		api.Use(s.requirePasswordChanged)

		api.With(s.requireRole(roleUser)).Get("/members", s.handleListMembers)
		api.With(s.requireRole(roleAdmin)).Post("/members", s.handleCreateAccount)
		api.With(s.requireRole(roleAdmin)).Patch("/members/{memberID}", s.handleUpdateAccount)
		api.With(s.requireRole(roleAdmin)).Put("/members/{memberID}/password", s.handleResetAccountPassword)
		api.With(s.requireRole(roleAdmin)).Delete("/members/{memberID}", s.handleDeleteAccount)
		api.With(s.requireRole(roleUser)).Get("/teams", s.handleListTeams)

		api.With(s.requireRole(roleUser)).Get("/agents", s.handleListAgents)
		api.With(s.requireRole(roleUser)).Get("/agents/{agentID}", s.handleGetAgent)
		api.With(s.requireRole(roleAdmin)).Post("/agents", s.handleCreateAgent)
		api.With(s.requireRole(roleAdmin)).Delete("/agents/{agentID}", s.handleDeleteAgent)

		api.With(s.requireRole(roleUser)).Get("/hosts", s.handleListHosts)
		api.With(s.requireRole(roleUser)).Get("/hosts/{hostID}", s.handleGetHost)
		api.With(s.requireRole(roleAdmin)).Delete("/hosts/{hostID}", s.handleDeleteHost)
		api.With(s.requireRole(roleAdmin)).Get("/hosts/{hostID}/runtime-sessions", s.handleListRuntimeSessions)

		api.With(s.requireRole(roleUser)).Get("/workspaces", s.handleListWorkspaces)
		api.With(s.requireRole(roleUser)).Get("/workspaces/{workspaceID}", s.handleGetWorkspace)
		api.With(s.requireRole(roleAdmin)).Post("/workspaces", s.handleCreateWorkspace)
		api.With(s.requireRole(roleUser)).Get("/workspaces/{workspaceID}/acl", s.handleListWorkspaceACL)
		api.With(s.requireRole(roleUser)).Put("/workspaces/{workspaceID}/acl", s.handleReplaceWorkspaceACL)
		api.With(s.requireRole(roleAdmin)).Delete("/workspaces/{workspaceID}", s.handleDeleteWorkspace)

		api.With(s.requireRole(roleUser)).Get("/boxes", s.handleListBoxes)
		api.With(s.requireRole(roleUser)).Get("/boxes/{boxID}", s.handleGetBox)
		api.With(s.requireRole(roleUser)).Post("/boxes", s.handleCreateBox)
		api.With(s.requireRole(roleUser)).Delete("/boxes/{boxID}", s.handleDeleteBox)
		api.With(s.requireRole(roleUser)).Patch("/boxes/{boxID}/model", s.handleUpdateBoxModel)
		api.With(s.requireRole(roleUser)).Patch("/boxes/{boxID}/visibility", s.handleUpdateBoxVisibility)
		api.With(s.requireRole(roleUser)).Get("/boxes/{boxID}/runtime-session/attachments", s.handleListRuntimeSessionAttachments)
		api.With(s.requireRole(roleUser)).Post("/boxes/{boxID}/runtime-session/detach", s.handleDetachRuntimeSession)
		api.With(s.requireRole(roleAdmin)).Post("/boxes/{boxID}/runtime-session/stop", s.handleStopRuntimeSession)
		api.With(s.requireRole(roleUser)).Get("/boxes/{boxID}/acl", s.handleListBoxACL)
		api.With(s.requireRole(roleUser)).Put("/boxes/{boxID}/acl", s.handleReplaceBoxACL)
		api.With(s.requireRole(roleUser)).Get("/boxes/{boxID}/messages", s.handleListMessages)
		api.With(s.requireRole(roleUser)).Post("/boxes/{boxID}/messages", s.handleSendMessage)
		api.With(s.requireRole(roleUser)).Delete("/boxes/{boxID}/messages/{messageID}", s.handleCancelQueuedMessage)
		api.With(s.requireRole(roleUser)).Get("/boxes/{boxID}/events", s.handleEvents)
		api.With(s.requireRole(roleUser)).Post("/boxes/{boxID}/interrupt", s.handleInterrupt)
		api.With(s.requireRole(roleUser)).Post("/boxes/{boxID}/stop", s.handleStop)
		api.With(s.requireRole(roleUser)).Post("/boxes/{boxID}/resume", s.handleResume)

		api.With(s.requireRole(roleUser)).Get("/approvals", s.handleListApprovals)
		api.With(s.requireRole(roleUser)).Post("/approvals/{approvalID}/decision", s.handleDecideApproval)

		api.With(s.requireRole(roleUser)).Get("/schedules", s.handleListSchedules)
		api.With(s.requireRole(roleUser)).Post("/schedules", s.handleCreateSchedule)
		api.With(s.requireRole(roleUser)).Patch("/schedules/{scheduleID}", s.handleUpdateSchedule)
		api.With(s.requireRole(roleUser)).Delete("/schedules/{scheduleID}", s.handleDeleteSchedule)
		api.With(s.requireRole(roleUser)).Get("/schedules/{scheduleID}/executions", s.handleListScheduleExecutions)

		api.With(s.requireRole(roleUser)).Get("/notifications", s.handleListNotifications)
		api.With(s.requireRole(roleUser)).Post("/notifications/{notificationID}/read", s.handleMarkNotificationRead)
		api.With(s.requireRole(roleUser)).Post("/notifications/read-all", s.handleMarkAllNotificationsRead)

		api.With(s.requireRole(roleUser)).Get("/boxes/{boxID}/subagents", s.handleListSubagents)
		api.With(s.requireRole(roleUser)).Get("/boxes/{boxID}/todos", s.handleListTodos)
		api.With(s.requireRole(roleUser)).Get("/boxes/{boxID}/artifacts", s.handleListArtifacts)
		api.With(s.requireRole(roleUser)).Get("/boxes/{boxID}/artifacts/{artifactID}/download", s.handleDownloadArtifact)
		api.With(s.requireRole(roleUser)).Get("/boxes/{boxID}/diff", s.handleWorkspaceDiff)
		api.With(s.requireRole(roleUser)).Get("/boxes/{boxID}/terminal", s.handleCommandTerminal)
		api.With(s.requireRole(roleUser)).Get("/boxes/{boxID}/command-terminal", s.handleCommandTerminal)
		api.With(s.requireRole(roleUser)).Get("/boxes/{boxID}/agent-terminal", s.handleAgentTerminal)
	})

	if s.staticRoot != nil {
		router.NotFound(s.handleStatic)
	}
	return router
}

func (s *Server) dispatch(command *domain.HostCommand) bool {
	if command == nil {
		return false
	}
	return s.hosts.dispatch(command.HostID, command)
}

func (s *Server) logError(operation string, err error, attributes ...any) {
	values := make([]any, 0, len(attributes)+4)
	values = append(values, "component", "agentbox-server", "operation", operation)
	values = append(values, attributes...)
	s.logger.Error(operation+" failed", append(values, "error", err)...)
}

func requestUser(request *http.Request) (domain.User, bool) {
	user, ok := request.Context().Value(userContextKey{}).(domain.User)
	return user, ok
}

type userContextKey struct{}

func (s *Server) handleHealth(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	if _, err := s.store.ListAgents(ctx, s.systemUser); err != nil {
		s.metrics.recordFailure("database", "database readiness check failed")
		writeProblem(writer, http.StatusServiceUnavailable, "not_ready", "database is unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleMeta(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, map[string]any{
		"serverVersion":    s.serverVersion,
		"apiVersion":       defaultAPIVersion,
		"minDaemonVersion": "0.1.0",
		"enabledRuntimes":  s.enabledRuntimes(),
		"runtimeModels":    s.runtimeModels,
		"currentUser": map[string]any{
			"id":                 user.ID,
			"login":              user.Login,
			"displayName":        user.DisplayName,
			"role":               user.Role,
			"status":             user.Status,
			"mustChangePassword": user.MustChangePassword,
		},
	})
}

func (s *Server) enabledRuntimes() []string {
	result := []string{"omp"}
	if s.enableCodex {
		result = append(result, "codex")
	}
	if s.enableClaude {
		result = append(result, "claude")
	}
	return result
}

func (s *Server) runtimeEnabled(runtimeType string) bool {
	switch runtimeType {
	case "omp":
		return true
	case "codex":
		return s.enableCodex
	case "claude":
		return s.enableClaude
	default:
		return false
	}
}

func cloneRuntimeModels(models map[string][]string) map[string][]string {
	result := make(map[string][]string, len(models)+2)
	for runtimeName, suggestions := range models {
		result[runtimeName] = append([]string(nil), suggestions...)
	}
	return result
}

func statusForStoreError(err error) (int, string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, store.ErrForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrInvalidState):
		return http.StatusConflict, "conflict"
	case errors.Is(err, store.ErrHostOffline):
		return http.StatusServiceUnavailable, "host_offline"
	case errors.Is(err, store.ErrRuntimeMissing):
		return http.StatusConflict, "runtime_missing"
	default:
		return http.StatusInternalServerError, "internal_error"
	}
}

func (s *Server) writeStoreError(writer http.ResponseWriter, operation string, err error) {
	statusCode, code := statusForStoreError(err)
	if statusCode == http.StatusInternalServerError {
		s.logError(operation, err)
		s.metrics.recordFailure("database", "database request failed")
	}
	message := http.StatusText(statusCode)
	writeProblem(writer, statusCode, code, message)
}
