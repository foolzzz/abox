package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	hostv1 "agentbox/api"
	"agentbox/internal/domain"
	"agentbox/internal/identity"
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
	Identity                identity.Extractor
	DevelopmentUser         domain.User
	EnrollmentToken         string
	ServerVersion           string
	StaticDir               string
	Logger                  *slog.Logger
	ApprovalPollInterval    time.Duration
	HibernationPollInterval time.Duration
	ReaperBatchSize         int
	HeartbeatInterval       time.Duration
	HostPendingCommandLimit int
	SSEReplayLimit          int
	SSESubscriberBuffer     int
}

type Server struct {
	hostv1.UnimplementedHostServiceServer

	store           store.Store
	identity        identity.Extractor
	developmentUser domain.User
	enrollmentToken string
	serverVersion   string
	staticDir       string
	logger          *slog.Logger

	approvalPollInterval    time.Duration
	hibernationPollInterval time.Duration
	reaperBatchSize         int
	heartbeatInterval       time.Duration
	hostCommandLimit        int
	replayLimit             int

	events  *eventHub
	hosts   *hostHub
	metrics *metrics
	router  http.Handler
}

func New(options Options) (*Server, error) {
	if options.Store == nil {
		return nil, errors.New("server store is required")
	}
	if options.Identity == nil {
		return nil, errors.New("server identity extractor is required")
	}
	if options.DevelopmentUser.ID == "" || options.DevelopmentUser.OrganizationID == "" {
		return nil, errors.New("server development user is required")
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

	metricSet := newMetrics()
	server := &Server{
		store:                   options.Store,
		identity:                options.Identity,
		developmentUser:         options.DevelopmentUser,
		enrollmentToken:         options.EnrollmentToken,
		serverVersion:           options.ServerVersion,
		staticDir:               options.StaticDir,
		logger:                  options.Logger,
		approvalPollInterval:    options.ApprovalPollInterval,
		hibernationPollInterval: options.HibernationPollInterval,
		reaperBatchSize:         options.ReaperBatchSize,
		heartbeatInterval:       options.HeartbeatInterval,
		hostCommandLimit:        options.HostPendingCommandLimit,
		replayLimit:             options.SSEReplayLimit,
		events:                  newEventHub(options.SSESubscriberBuffer, metricSet),
		hosts:                   newHostHub(metricSet),
		metrics:                 metricSet,
	}
	server.router = server.routes()
	return server, nil
}

func (s *Server) Handler() http.Handler {
	return s.router
}

func (s *Server) RegisterGRPC(registrar grpc.ServiceRegistrar) {
	hostv1.RegisterHostServiceServer(registrar, s)
}

func (s *Server) Run(ctx context.Context) {
	go s.runApprovalReaper(ctx)
	go s.runHibernationReaper(ctx)
	<-ctx.Done()
}

func (s *Server) routes() http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.Recoverer)
	router.Use(s.observeHTTP)

	router.Get("/healthz", s.handleHealth)
	router.Get("/readyz", s.handleReady)
	router.Get("/metrics", s.metrics.handle)

	router.Route("/api/v1", func(api chi.Router) {
		api.Use(s.authenticate)
		api.Get("/meta", s.handleMeta)

		api.With(s.requireRole(roleViewer)).Get("/members", s.handleListMembers)
		api.With(s.requireRole(roleAdmin)).Patch("/members/{memberID}/role", s.handleUpdateMemberRole)
		api.With(s.requireRole(roleViewer)).Get("/teams", s.handleListTeams)

		api.With(s.requireRole(roleViewer)).Get("/agents", s.handleListAgents)
		api.With(s.requireRole(roleViewer)).Get("/agents/{agentID}", s.handleGetAgent)
		api.With(s.requireRole(roleAdmin)).Post("/agents", s.handleCreateAgent)

		api.With(s.requireRole(roleViewer)).Get("/hosts", s.handleListHosts)
		api.With(s.requireRole(roleViewer)).Get("/hosts/{hostID}", s.handleGetHost)

		api.With(s.requireRole(roleViewer)).Get("/workspaces", s.handleListWorkspaces)
		api.With(s.requireRole(roleViewer)).Get("/workspaces/{workspaceID}", s.handleGetWorkspace)
		api.With(s.requireRole(roleAdmin)).Post("/workspaces", s.handleCreateWorkspace)
		api.With(s.requireRole(roleViewer)).Get("/workspaces/{workspaceID}/acl", s.handleListWorkspaceACL)
		api.With(s.requireRole(roleViewer)).Put("/workspaces/{workspaceID}/acl", s.handleReplaceWorkspaceACL)

		api.With(s.requireRole(roleViewer)).Get("/boxes", s.handleListBoxes)
		api.With(s.requireRole(roleViewer)).Get("/boxes/{boxID}", s.handleGetBox)
		api.With(s.requireRole(roleViewer)).Post("/boxes", s.handleCreateBox)
		api.With(s.requireRole(roleViewer)).Get("/boxes/{boxID}/acl", s.handleListBoxACL)
		api.With(s.requireRole(roleViewer)).Put("/boxes/{boxID}/acl", s.handleReplaceBoxACL)
		api.With(s.requireRole(roleViewer)).Get("/boxes/{boxID}/messages", s.handleListMessages)
		api.With(s.requireRole(roleViewer)).Post("/boxes/{boxID}/messages", s.handleSendMessage)
		api.With(s.requireRole(roleViewer)).Delete("/boxes/{boxID}/messages/{messageID}", s.handleCancelQueuedMessage)
		api.With(s.requireRole(roleViewer)).Get("/boxes/{boxID}/events", s.handleEvents)
		api.With(s.requireRole(roleViewer)).Post("/boxes/{boxID}/interrupt", s.handleInterrupt)
		api.With(s.requireRole(roleViewer)).Post("/boxes/{boxID}/stop", s.handleStop)
		api.With(s.requireRole(roleViewer)).Post("/boxes/{boxID}/resume", s.handleResume)

		api.With(s.requireRole(roleViewer)).Get("/approvals", s.handleListApprovals)
		api.With(s.requireRole(roleViewer)).Post("/approvals/{approvalID}/decision", s.handleDecideApproval)
	})

	if strings.TrimSpace(s.staticDir) != "" {
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
	if _, err := s.store.ListAgents(ctx, s.developmentUser); err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, "not_ready", "database is unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleMeta(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	writeJSON(writer, http.StatusOK, map[string]any{
		"serverVersion":    s.serverVersion,
		"apiVersion":       defaultAPIVersion,
		"minDaemonVersion": "0.1.0",
		"currentUser": map[string]string{
			"id":          user.ID,
			"login":       user.Login,
			"displayName": user.DisplayName,
			"role":        user.Role,
		},
	})
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
	}
	message := http.StatusText(statusCode)
	writeProblem(writer, statusCode, code, message)
}
