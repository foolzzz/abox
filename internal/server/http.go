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

func (s *Server) handleListMembers(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	members, err := s.store.ListMembers(request.Context(), user)
	if err != nil {
		s.writeStoreError(writer, "list members", err)
		return
	}
	writeJSON(writer, http.StatusOK, members)
}

func (s *Server) handleUpdateMemberRole(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if levelForRole(body.Role) == 0 {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", "role must be owner, admin, operator, or viewer")
		return
	}
	user, _ := requestUser(request)
	member, err := s.store.UpdateMemberRole(request.Context(), user, chi.URLParam(request, "memberID"), body.Role)
	if err != nil {
		s.writeStoreError(writer, "update member role", err)
		return
	}
	writeJSON(writer, http.StatusOK, member)
}

func (s *Server) handleListTeams(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	teams, err := s.store.ListTeams(request.Context(), user)
	if err != nil {
		s.writeStoreError(writer, "list teams", err)
		return
	}
	writeJSON(writer, http.StatusOK, teams)
}

type aclEntryRequest struct {
	UserID string `json:"userId"`
	TeamID string `json:"teamId"`
	Role   string `json:"role"`
}

func decodeACLReplacement(request *http.Request) ([]domain.ResourceACLEntryInput, error) {
	var body struct {
		Entries *[]aclEntryRequest `json:"entries"`
	}
	if err := decodeJSON(request, &body); err != nil {
		return nil, err
	}
	if body.Entries == nil {
		return nil, errors.New("entries is required")
	}
	result := make([]domain.ResourceACLEntryInput, len(*body.Entries))
	for _, entry := range *body.Entries {
		if (strings.TrimSpace(entry.UserID) == "") == (strings.TrimSpace(entry.TeamID) == "") {
			return nil, errors.New("each entry must identify exactly one userId or teamId")
		}
		switch strings.ToLower(strings.TrimSpace(entry.Role)) {
		case "owner", "operator", "viewer":
		default:
			return nil, errors.New("entry role must be owner, operator, or viewer")
		}
	}
	for index, entry := range *body.Entries {
		result[index] = domain.ResourceACLEntryInput{UserID: entry.UserID, TeamID: entry.TeamID, Role: entry.Role}
	}
	return result, nil
}

func (s *Server) handleListWorkspaceACL(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	entries, err := s.store.ListWorkspaceACL(request.Context(), user, chi.URLParam(request, "workspaceID"))
	if err != nil {
		s.writeStoreError(writer, "list workspace acl", err)
		return
	}
	writeJSON(writer, http.StatusOK, entries)
}

func (s *Server) handleReplaceWorkspaceACL(writer http.ResponseWriter, request *http.Request) {
	entries, err := decodeACLReplacement(request)
	if err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	user, _ := requestUser(request)
	result, err := s.store.ReplaceWorkspaceACL(request.Context(), user, chi.URLParam(request, "workspaceID"), entries)
	if err != nil {
		s.writeStoreError(writer, "replace workspace acl", err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) handleListBoxACL(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	entries, err := s.store.ListBoxACL(request.Context(), user, chi.URLParam(request, "boxID"))
	if err != nil {
		s.writeStoreError(writer, "list box acl", err)
		return
	}
	writeJSON(writer, http.StatusOK, entries)
}

func (s *Server) handleReplaceBoxACL(writer http.ResponseWriter, request *http.Request) {
	entries, err := decodeACLReplacement(request)
	if err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	user, _ := requestUser(request)
	result, err := s.store.ReplaceBoxACL(request.Context(), user, chi.URLParam(request, "boxID"), entries)
	if err != nil {
		s.writeStoreError(writer, "replace box acl", err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
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
	body.RuntimeType = strings.TrimSpace(body.RuntimeType)
	if body.RuntimeType == "" {
		body.RuntimeType = "omp"
	}
	if !s.runtimeEnabled(body.RuntimeType) {
		writeProblem(writer, http.StatusBadRequest, "runtime_disabled", "the first release enables OMP and Codex; Claude requires enableClaude=true")
		return
	}
	body.Model = strings.TrimSpace(body.Model)
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
	host, err := s.store.GetHost(request.Context(), user, chi.URLParam(request, "hostID"))
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
	workspace, err := s.store.GetWorkspace(request.Context(), user, chi.URLParam(request, "workspaceID"))
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
	workspace, command, err := s.store.CreateWorkspace(request.Context(), user, domain.CreateWorkspaceInput{
		HostID: body.HostID,
		Name:   body.Name,
		Path:   body.Path,
		Kind:   body.Kind,
	})
	if err != nil {
		s.writeStoreError(writer, "create workspace", err)
		return
	}
	if command != nil {
		s.dispatch(command)
		writeJSON(writer, http.StatusAccepted, workspace)
		return
	}
	writeJSON(writer, http.StatusOK, workspace)
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
	agent, err := s.store.GetAgent(request.Context(), user, body.AgentID)
	if err != nil {
		s.writeStoreError(writer, "get box agent", err)
		return
	}
	if !s.runtimeEnabled(agent.RuntimeType) {
		writeProblem(writer, http.StatusBadRequest, "runtime_disabled", "the selected agent runtime is disabled by server configuration")
		return
	}
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

func (s *Server) handleDeleteBox(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	boxID := chi.URLParam(request, "boxID")
	box, err := s.store.GetBox(request.Context(), user, boxID)
	if err != nil {
		s.writeStoreError(writer, "get box for deletion", err)
		return
	}
	command, err := s.store.DeleteBox(request.Context(), user, boxID)
	if err != nil {
		s.writeStoreError(writer, "delete box", err)
		return
	}
	s.terminateAgentTerminal(box)
	if command != nil {
		s.dispatch(command)
		writer.WriteHeader(http.StatusAccepted)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
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
		Content             string                     `json:"content"`
		Delivery            domain.Delivery            `json:"delivery"`
		PresentationContext domain.PresentationContext `json:"presentationContext"`
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
	if err := validatePresentationContext(body.PresentationContext); err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_presentation_context", err.Error())
		return
	}
	user, _ := requestUser(request)
	message, _, command, err := s.store.SendMessage(request.Context(), user, chi.URLParam(request, "boxID"), domain.SendMessageInput{
		Content:             body.Content,
		Delivery:            body.Delivery,
		PresentationContext: body.PresentationContext,
		IdempotencyKey:      idempotencyKey,
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

func (s *Server) handleCancelQueuedMessage(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	message, err := s.store.CancelQueuedMessage(
		request.Context(), user, chi.URLParam(request, "boxID"), chi.URLParam(request, "messageID"),
	)
	if err != nil {
		s.writeStoreError(writer, "cancel queued message", err)
		return
	}
	writeJSON(writer, http.StatusOK, toMessageResponse(message))
}

type messageResponse struct {
	ID                  string          `json:"id"`
	BoxID               string          `json:"boxId"`
	RunID               string          `json:"runId,omitempty"`
	BoxSeq              int64           `json:"boxSeq"`
	AuthorName          string          `json:"authorName,omitempty"`
	Role                string          `json:"role"`
	Delivery            domain.Delivery `json:"delivery,omitempty"`
	Status              string          `json:"status"`
	Content             string          `json:"content"`
	PresentationContext json.RawMessage `json:"presentationContext,omitempty"`
	CreatedAt           time.Time       `json:"createdAt"`
}

func toMessageResponse(message domain.Message) messageResponse {
	return messageResponse{
		ID:                  message.ID,
		BoxID:               message.BoxID,
		RunID:               message.RunID,
		BoxSeq:              message.BoxSeq,
		AuthorName:          message.AuthorName,
		Role:                message.Role,
		Delivery:            message.Delivery,
		Status:              message.Status,
		Content:             message.PlainText,
		PresentationContext: message.PresentationContext,
		CreatedAt:           message.CreatedAt,
	}
}

func validatePresentationContext(value domain.PresentationContext) error {
	if value.ViewportWidth < 0 || value.ViewportWidth > 20000 || value.ViewportHeight < 0 || value.ViewportHeight > 20000 {
		return errors.New("viewport dimensions must be between 0 and 20000")
	}
	if value.DeviceClass != "" && value.DeviceClass != "mobile" && value.DeviceClass != "tablet" && value.DeviceClass != "desktop" {
		return errors.New("deviceClass must be mobile, tablet, or desktop")
	}
	if value.Orientation != "" && value.Orientation != "portrait" && value.Orientation != "landscape" {
		return errors.New("orientation must be portrait or landscape")
	}
	if len(value.Locale) > 64 || len(value.Timezone) > 128 || len(value.Surface) > 64 {
		return errors.New("locale, timezone, or surface is too long")
	}
	return nil
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
	if box.Status != domain.BoxHibernated && box.Status != domain.BoxError {
		writeProblem(writer, http.StatusConflict, "conflict", "box is not resumable in its current state")
		return
	}
	workspace, err := s.store.GetWorkspaceForOrganization(request.Context(), user.OrganizationID, box.WorkspaceID)
	if err != nil {
		s.writeStoreError(writer, "get workspace for resume", err)
		return
	}
	agent, err := s.store.GetAgent(request.Context(), user, box.AgentID)
	if err != nil {
		s.writeStoreError(writer, "get agent for resume", err)
		return
	}
	if !s.runtimeEnabled(box.RuntimeType) {
		writeProblem(writer, http.StatusBadRequest, "runtime_disabled", "the selected agent runtime is disabled by server configuration")
		return
	}
	approvalMode := ""
	var approvalPolicy struct {
		Mode string `json:"mode"`
	}
	if len(agent.ApprovalPolicy) > 0 {
		_ = json.Unmarshal(agent.ApprovalPolicy, &approvalPolicy)
		approvalMode = strings.TrimSpace(approvalPolicy.Mode)
	}
	if approvalMode == "" && box.RuntimeType == "omp" {
		approvalMode = "always-ask"
	}
	payload, err := json.Marshal(map[string]any{
		"runtime":      box.RuntimeType,
		"workspace":    workspace.Path,
		"model":        agent.Model,
		"approvalMode": approvalMode,
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
	user, _ := requestUser(request)
	key := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if key == "" {
		key = fmt.Sprintf("%s:%s:%d", commandType, box.ID, box.Version)
	}
	command, err := s.store.CreateHostCommand(request.Context(), user, domain.HostCommand{
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
	if serveStaticFile(s.staticRoot, cleanPath, writer, request) {
		return
	}
	if !serveStaticFile(s.staticRoot, "index.html", writer, request) {
		writeProblem(writer, http.StatusNotFound, "not_found", "resource not found")
	}
}

func serveStaticFile(root *os.Root, name string, writer http.ResponseWriter, request *http.Request) bool {
	if root == nil || name == "" {
		return false
	}
	file, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		return false
	}
	http.ServeContent(writer, request, name, info.ModTime(), file)
	return true
}
