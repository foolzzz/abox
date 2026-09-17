package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"agentbox/internal/domain"
	"agentbox/internal/identity"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type roleLevel int

const (
	roleViewer roleLevel = iota + 1
	roleOperator
	roleAdmin
	roleOwner
)

func levelForRole(role string) roleLevel {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "owner":
		return roleOwner
	case "admin":
		return roleAdmin
	case "operator":
		return roleOperator
	case "viewer":
		return roleViewer
	default:
		return 0
	}
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		principal, err := s.identity.Extract(request)
		if err != nil {
			statusCode := http.StatusUnauthorized
			if errors.Is(err, identity.ErrUntrustedSource) {
				statusCode = http.StatusForbidden
			}
			writeProblem(writer, statusCode, "unauthenticated", "a trusted Tailscale or development identity is required")
			return
		}
		user, err := s.store.EnsureDevelopmentTenant(request.Context(), principal.LoginName)
		if err != nil {
			s.logError("resolve identity", err, "login", principal.LoginName)
			writeProblem(writer, http.StatusServiceUnavailable, "identity_unavailable", "identity could not be resolved")
			return
		}
		next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), userContextKey{}, user)))
	})
}

func (s *Server) requireRole(minimum roleLevel) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			user, ok := requestUser(request)
			if !ok {
				writeProblem(writer, http.StatusUnauthorized, "unauthenticated", "authentication is required")
				return
			}
			if levelForRole(user.Role) < minimum {
				writeProblem(writer, http.StatusForbidden, "forbidden", "the current role cannot perform this operation")
				return
			}
			next.ServeHTTP(writer, request)
		})
	}
}

func (s *Server) handleListAgents(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	agents, err := s.store.ListAgents(request.Context(), user)
	if err != nil {
		s.writeStoreError(writer, "list agents", err)
		return
	}
	writeJSON(writer, http.StatusOK, agents)
}

func (s *Server) handleGetAgent(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	agent, err := s.store.GetAgent(request.Context(), user, chi.URLParam(request, "agentID"))
	if err != nil {
		s.writeStoreError(writer, "get agent", err)
		return
	}
	writeJSON(writer, http.StatusOK, agent)
}

func (s *Server) handleCreateAgent(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Name         string `json:"name"`
		RuntimeType  string `json:"runtimeType"`
		Model        string `json:"model"`
		SystemPrompt string `json:"systemPrompt"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.SystemPrompt) == "" {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", "name and systemPrompt are required")
		return
	}
	if body.RuntimeType != "omp" && body.RuntimeType != "claude" && body.RuntimeType != "acp" {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", "runtimeType must be omp, claude, or acp")
		return
	}
	user, _ := requestUser(request)
	agent, err := s.store.CreateAgent(request.Context(), user, domain.CreateAgentInput{
		Name:         body.Name,
		RuntimeType:  body.RuntimeType,
		Model:        body.Model,
		SystemPrompt: body.SystemPrompt,
	})
	if err != nil {
		s.writeStoreError(writer, "create agent", err)
		return
	}
	writeJSON(writer, http.StatusCreated, agent)
}

func (s *Server) handleListHosts(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	hosts, err := s.store.ListHosts(request.Context(), user)
	if err != nil {
		s.writeStoreError(writer, "list hosts", err)
		return
	}
	writeJSON(writer, http.StatusOK, hosts)
}

func (s *Server) handleGetHost(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	host, err := s.store.GetHost(request.Context(), user.OrganizationID, chi.URLParam(request, "hostID"))
	if err != nil {
		s.writeStoreError(writer, "get host", err)
		return
	}
	writeJSON(writer, http.StatusOK, host)
}

func (s *Server) handleListWorkspaces(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	workspaces, err := s.store.ListWorkspaces(request.Context(), user)
	if err != nil {
		s.writeStoreError(writer, "list workspaces", err)
		return
	}
	writeJSON(writer, http.StatusOK, workspaces)
}

func (s *Server) handleGetWorkspace(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	workspace, err := s.store.GetWorkspace(request.Context(), user.OrganizationID, chi.URLParam(request, "workspaceID"))
	if err != nil {
		s.writeStoreError(writer, "get workspace", err)
		return
	}
	writeJSON(writer, http.StatusOK, workspace)
}

func (s *Server) handleCreateWorkspace(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		HostID string `json:"hostId"`
		Name   string `json:"name"`
		Path   string `json:"path"`
		Kind   string `json:"kind"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(body.HostID) == "" || strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.Path) == "" {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", "hostId, name, and path are required")
		return
	}
	if body.Kind == "" {
		body.Kind = "existing"
	}
	user, _ := requestUser(request)
	workspace, err := s.store.CreateWorkspace(request.Context(), user, domain.CreateWorkspaceInput{
		HostID: body.HostID,
		Name:   body.Name,
		Path:   body.Path,
		Kind:   body.Kind,
	})
	if err != nil {
		s.writeStoreError(writer, "create workspace", err)
		return
	}
	writeJSON(writer, http.StatusCreated, workspace)
}

func (s *Server) handleListBoxes(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	boxes, err := s.store.ListBoxes(request.Context(), user)
	if err != nil {
		s.writeStoreError(writer, "list boxes", err)
		return
	}
	writeJSON(writer, http.StatusOK, boxes)
}

func (s *Server) handleGetBox(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	box, err := s.store.GetBox(request.Context(), user, chi.URLParam(request, "boxID"))
	if err != nil {
		s.writeStoreError(writer, "get box", err)
		return
	}
	writeJSON(writer, http.StatusOK, box)
}

func (s *Server) handleCreateBox(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Name        string `json:"name"`
		AgentID     string `json:"agentId"`
		HostID      string `json:"hostId"`
		WorkspaceID string `json:"workspaceId"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.AgentID) == "" || strings.TrimSpace(body.HostID) == "" || strings.TrimSpace(body.WorkspaceID) == "" {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", "name, agentId, hostId, and workspaceId are required")
		return
	}
	user, _ := requestUser(request)
	box, err := s.store.CreateBox(request.Context(), user, domain.CreateBoxInput{
		Name:        body.Name,
		AgentID:     body.AgentID,
		HostID:      body.HostID,
		WorkspaceID: body.WorkspaceID,
	})
	if err != nil {
		s.writeStoreError(writer, "create box", err)
		return
	}
	writeJSON(writer, http.StatusCreated, box)
}

func (s *Server) handleListMessages(writer http.ResponseWriter, request *http.Request) {
	limit, err := queryLimit(request, 100, 500)
	if err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	user, _ := requestUser(request)
	messages, err := s.store.ListMessages(request.Context(), user, chi.URLParam(request, "boxID"), limit)
	if err != nil {
		s.writeStoreError(writer, "list messages", err)
		return
	}
	responses := make([]messageResponse, len(messages))
	for index := range messages {
		responses[index] = toMessageResponse(messages[index])
	}
	writeJSON(writer, http.StatusOK, responses)
}

func (s *Server) handleSendMessage(writer http.ResponseWriter, request *http.Request) {
	idempotencyKey := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeProblem(writer, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key is required")
		return
	}
	var body struct {
		Content  string          `json:"content"`
		Delivery domain.Delivery `json:"delivery"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", "content is required")
		return
	}
	if body.Delivery != domain.DeliveryPrompt && body.Delivery != domain.DeliverySteer && body.Delivery != domain.DeliveryFollowUp {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", "delivery must be prompt, steer, or follow_up")
		return
	}
	user, _ := requestUser(request)
	message, _, command, err := s.store.SendMessage(request.Context(), user, chi.URLParam(request, "boxID"), domain.SendMessageInput{
		Content:        body.Content,
		Delivery:       body.Delivery,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		s.writeStoreError(writer, "send message", err)
		return
	}
	if command != nil {
		s.dispatch(command)
	}
	writeJSON(writer, http.StatusAccepted, toMessageResponse(message))
}

type messageResponse struct {
	ID         string          `json:"id"`
	BoxID      string          `json:"boxId"`
	RunID      string          `json:"runId,omitempty"`
	BoxSeq     int64           `json:"boxSeq"`
	AuthorName string          `json:"authorName,omitempty"`
	Role       string          `json:"role"`
	Delivery   domain.Delivery `json:"delivery,omitempty"`
	Status     string          `json:"status"`
	Content    string          `json:"content"`
	CreatedAt  time.Time       `json:"createdAt"`
}

func toMessageResponse(message domain.Message) messageResponse {
	return messageResponse{
		ID:         message.ID,
		BoxID:      message.BoxID,
		RunID:      message.RunID,
		BoxSeq:     message.BoxSeq,
		AuthorName: message.AuthorName,
		Role:       message.Role,
		Delivery:   message.Delivery,
		Status:     message.Status,
		Content:    message.PlainText,
		CreatedAt:  message.CreatedAt,
	}
}

func (s *Server) handleInterrupt(writer http.ResponseWriter, request *http.Request) {
	s.handleBoxCommand(writer, request, boxCommandSpec{
		commandType: "runtime.interrupt",
		payload:     json.RawMessage(`{}`),
		allowed:     []domain.BoxStatus{domain.BoxRunning, domain.BoxWaitingApproval},
	})
}

func (s *Server) handleStop(writer http.ResponseWriter, request *http.Request) {
	s.handleBoxCommand(writer, request, boxCommandSpec{
		commandType: "runtime.stop",
		payload:     json.RawMessage(`{"mode":"graceful"}`),
		disallowed:  []domain.BoxStatus{domain.BoxTerminated},
	})
}

func (s *Server) handleResume(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	box, err := s.store.GetBox(request.Context(), user, chi.URLParam(request, "boxID"))
	if err != nil {
		s.writeStoreError(writer, "get box for resume", err)
		return
	}
	if !mayOperateBox(user, box) {
		writeProblem(writer, http.StatusForbidden, "forbidden", "the current user cannot operate this box")
		return
	}
	if box.Status != domain.BoxHibernated && box.Status != domain.BoxError {
		writeProblem(writer, http.StatusConflict, "conflict", "box is not resumable in its current state")
		return
	}
	workspace, err := s.store.GetWorkspace(request.Context(), user.OrganizationID, box.WorkspaceID)
	if err != nil {
		s.writeStoreError(writer, "get workspace for resume", err)
		return
	}
	agent, err := s.store.GetAgent(request.Context(), user, box.AgentID)
	if err != nil {
		s.writeStoreError(writer, "get agent for resume", err)
		return
	}
	payload, err := json.Marshal(map[string]any{
		"runtime":   box.RuntimeType,
		"workspace": workspace.Path,
		"model":     agent.Model,
	})
	if err != nil {
		s.logError("encode resume command", err, "box_id", box.ID)
		writeProblem(writer, http.StatusInternalServerError, "internal_error", "resume command could not be encoded")
		return
	}
	s.persistBoxCommand(writer, request, box, "runtime.start", payload, uuid.NewString())
}

type boxCommandSpec struct {
	commandType string
	payload     json.RawMessage
	allowed     []domain.BoxStatus
	disallowed  []domain.BoxStatus
}

func (s *Server) handleBoxCommand(writer http.ResponseWriter, request *http.Request, spec boxCommandSpec) {
	user, _ := requestUser(request)
	box, err := s.store.GetBox(request.Context(), user, chi.URLParam(request, "boxID"))
	if err != nil {
		s.writeStoreError(writer, "get box for command", err)
		return
	}
	if !mayOperateBox(user, box) {
		writeProblem(writer, http.StatusForbidden, "forbidden", "the current user cannot operate this box")
		return
	}
	if len(spec.allowed) > 0 && !containsBoxStatus(spec.allowed, box.Status) {
		writeProblem(writer, http.StatusConflict, "conflict", "box cannot accept this command in its current state")
		return
	}
	if containsBoxStatus(spec.disallowed, box.Status) {
		writeProblem(writer, http.StatusConflict, "conflict", "box cannot accept this command in its current state")
		return
	}
	s.persistBoxCommand(writer, request, box, spec.commandType, spec.payload, "")
}

func (s *Server) persistBoxCommand(writer http.ResponseWriter, request *http.Request, box domain.Box, commandType string, payload json.RawMessage, runtimeInstanceID string) {
	key := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if key == "" {
		key = fmt.Sprintf("%s:%s:%d", commandType, box.ID, box.Version)
	}
	command, err := s.store.CreateHostCommand(request.Context(), domain.HostCommand{
		ID:                uuid.NewString(),
		OrganizationID:    box.OrganizationID,
		HostID:            box.HostID,
		BoxID:             box.ID,
		RuntimeInstanceID: runtimeInstanceID,
		CommandType:       commandType,
		Payload:           payload,
		IdempotencyKey:    key,
		Status:            "pending",
	})
	if err != nil {
		s.writeStoreError(writer, "create host command", err)
		return
	}
	s.dispatch(&command)
	writeJSON(writer, http.StatusAccepted, command)
}

func mayOperateBox(user domain.User, box domain.Box) bool {
	return levelForRole(user.Role) >= roleAdmin || box.OwnerUserID == user.ID
}

func containsBoxStatus(statuses []domain.BoxStatus, current domain.BoxStatus) bool {
	for _, candidate := range statuses {
		if candidate == current {
			return true
		}
	}
	return false
}

func (s *Server) handleListApprovals(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	approvals, err := s.store.ListApprovals(request.Context(), user)
	if err != nil {
		s.writeStoreError(writer, "list approvals", err)
		return
	}
	writeJSON(writer, http.StatusOK, approvals)
}

func (s *Server) handleDecideApproval(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Decision string `json:"decision"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if body.Decision != "approved" && body.Decision != "denied" {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", "decision must be approved or denied")
		return
	}
	user, _ := requestUser(request)
	approval, command, err := s.store.ResolveApproval(request.Context(), user, chi.URLParam(request, "approvalID"), body.Decision)
	if err != nil {
		s.writeStoreError(writer, "resolve approval", err)
		return
	}
	if command != nil {
		s.dispatch(command)
	}
	writeJSON(writer, http.StatusOK, approval)
}

func decodeJSON(request *http.Request, destination any) error {
	request.Body = http.MaxBytesReader(nil, request.Body, 1<<20)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

func queryLimit(request *http.Request, fallback, maximum int) (int, error) {
	value := strings.TrimSpace(request.URL.Query().Get("limit"))
	if value == "" {
		return fallback, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit <= 0 || limit > maximum {
		return 0, fmt.Errorf("limit must be between 1 and %d", maximum)
	}
	return limit, nil
}

func writeJSON(writer http.ResponseWriter, statusCode int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(statusCode)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeProblem(writer http.ResponseWriter, statusCode int, code, message string) {
	writeJSON(writer, statusCode, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

func (s *Server) handleStatic(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writeProblem(writer, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	if strings.HasPrefix(request.URL.Path, "/api/") || request.URL.Path == "/healthz" || request.URL.Path == "/readyz" || request.URL.Path == "/metrics" {
		writeProblem(writer, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	cleanPath := strings.TrimPrefix(path.Clean("/"+request.URL.Path), "/")
	candidate := filepath.Join(s.staticDir, filepath.FromSlash(cleanPath))
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		http.ServeFile(writer, request, candidate)
		return
	}
	indexPath := filepath.Join(s.staticDir, "index.html")
	if info, err := os.Stat(indexPath); err != nil || info.IsDir() {
		writeProblem(writer, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	http.ServeFile(writer, request, indexPath)
}
