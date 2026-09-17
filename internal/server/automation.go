package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"agentbox/internal/domain"
	"github.com/go-chi/chi/v5"
)

func (s *Server) handleListSchedules(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	result, err := s.store.ListSchedules(request.Context(), user)
	if err != nil {
		s.writeStoreError(writer, "list schedules", err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) handleCreateSchedule(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Name              string `json:"name"`
		AgentID           string `json:"agentId"`
		HostID            string `json:"hostId"`
		WorkspaceID       string `json:"workspaceId"`
		CronExpression    string `json:"cronExpression"`
		Timezone          string `json:"timezone"`
		PromptTemplate    string `json:"promptTemplate"`
		ConcurrencyPolicy string `json:"concurrencyPolicy"`
		Status            string `json:"status"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	user, _ := requestUser(request)
	result, err := s.store.CreateSchedule(request.Context(), user, domain.CreateScheduleInput{
		Name: body.Name, AgentID: body.AgentID, HostID: body.HostID,
		WorkspaceID: body.WorkspaceID, CronExpression: body.CronExpression,
		Timezone: body.Timezone, PromptTemplate: body.PromptTemplate,
		ConcurrencyPolicy: body.ConcurrencyPolicy, Status: body.Status,
	})
	if err != nil {
		s.writeStoreError(writer, "create schedule", err)
		return
	}
	writeJSON(writer, http.StatusCreated, result)
}

func (s *Server) handleUpdateSchedule(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Name              *string `json:"name"`
		AgentID           *string `json:"agentId"`
		HostID            *string `json:"hostId"`
		WorkspaceID       *string `json:"workspaceId"`
		CronExpression    *string `json:"cronExpression"`
		Timezone          *string `json:"timezone"`
		PromptTemplate    *string `json:"promptTemplate"`
		ConcurrencyPolicy *string `json:"concurrencyPolicy"`
		Status            *string `json:"status"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	user, _ := requestUser(request)
	result, err := s.store.UpdateSchedule(request.Context(), user, chi.URLParam(request, "scheduleID"), domain.UpdateScheduleInput{
		Name: body.Name, AgentID: body.AgentID, HostID: body.HostID,
		WorkspaceID: body.WorkspaceID, CronExpression: body.CronExpression,
		Timezone: body.Timezone, PromptTemplate: body.PromptTemplate,
		ConcurrencyPolicy: body.ConcurrencyPolicy, Status: body.Status,
	})
	if err != nil {
		s.writeStoreError(writer, "update schedule", err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) handleDeleteSchedule(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	if err := s.store.DeleteSchedule(request.Context(), user, chi.URLParam(request, "scheduleID")); err != nil {
		s.writeStoreError(writer, "delete schedule", err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListScheduleExecutions(writer http.ResponseWriter, request *http.Request) {
	limit, err := queryLimit(request, 100, 500)
	if err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	user, _ := requestUser(request)
	result, err := s.store.ListScheduleExecutions(request.Context(), user, chi.URLParam(request, "scheduleID"), limit)
	if err != nil {
		s.writeStoreError(writer, "list schedule executions", err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) handleScheduleWebhook(writer http.ResponseWriter, request *http.Request) {
	if s.webhookSecret == "" {
		writeProblem(writer, http.StatusServiceUnavailable, "webhook_unavailable", "webhook signing is not configured")
		return
	}
	timestampText := strings.TrimSpace(request.Header.Get("X-Agentbox-Timestamp"))
	timestamp, err := strconv.ParseInt(timestampText, 10, 64)
	if err != nil || time.Since(time.Unix(timestamp, 0)).Abs() > 5*time.Minute {
		writeProblem(writer, http.StatusUnauthorized, "invalid_signature", "webhook timestamp is invalid or expired")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 1<<20))
	if err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", "webhook body is invalid")
		return
	}
	scheduleID := chi.URLParam(request, "scheduleID")
	mac := hmac.New(sha256.New, []byte(s.webhookSecret))
	_, _ = fmt.Fprintf(mac, "%s\n%s\n", timestampText, scheduleID)
	_, _ = mac.Write(body)
	expected := mac.Sum(nil)
	signature := strings.TrimPrefix(strings.TrimSpace(request.Header.Get("X-Agentbox-Signature")), "v1=")
	provided, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(expected, provided) {
		writeProblem(writer, http.StatusUnauthorized, "invalid_signature", "webhook signature is invalid")
		return
	}
	idempotencyKey := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeProblem(writer, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key is required")
		return
	}
	digest := sha256.Sum256(body)
	execution, commands, err := s.store.TriggerScheduleWebhook(request.Context(), scheduleID, idempotencyKey, hex.EncodeToString(digest[:]))
	if err != nil {
		s.writeStoreError(writer, "trigger schedule webhook", err)
		return
	}
	for index := range commands {
		s.dispatch(&commands[index])
	}
	queued, dispatchErr := s.store.DispatchAutomationRuns(request.Context(), s.reaperBatchSize)
	if dispatchErr != nil {
		s.logError("dispatch webhook run", dispatchErr, "schedule_id", scheduleID)
		s.metrics.recordFailure("automation scheduler", "automation scheduler failed")
	} else {
		s.observeScheduleDispatches(queued)
	}
	for index := range queued {
		s.dispatch(&queued[index])
	}
	writeJSON(writer, http.StatusAccepted, execution)
}

func (s *Server) handleListNotifications(writer http.ResponseWriter, request *http.Request) {
	limit, err := queryLimit(request, 100, 500)
	if err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	user, _ := requestUser(request)
	result, err := s.store.ListNotifications(request.Context(), user, limit)
	if err != nil {
		s.writeStoreError(writer, "list notifications", err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) handleMarkNotificationRead(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	if err := s.store.MarkNotificationRead(request.Context(), user, chi.URLParam(request, "notificationID")); err != nil {
		s.writeStoreError(writer, "mark notification read", err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMarkAllNotificationsRead(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	if err := s.store.MarkAllNotificationsRead(request.Context(), user); err != nil {
		s.writeStoreError(writer, "mark all notifications read", err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListSubagents(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	result, err := s.store.ListSubagents(request.Context(), user, chi.URLParam(request, "boxID"))
	if err != nil {
		s.writeStoreError(writer, "list subagents", err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) handleListTodos(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	result, err := s.store.ListTodos(request.Context(), user, chi.URLParam(request, "boxID"))
	if err != nil {
		s.writeStoreError(writer, "list todos", err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) handleListArtifacts(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	result, err := s.store.ListArtifacts(request.Context(), user, chi.URLParam(request, "boxID"))
	if err != nil {
		s.writeStoreError(writer, "list artifacts", err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) handleDownloadArtifact(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	artifact, err := s.store.GetArtifactForDownload(request.Context(), user, chi.URLParam(request, "boxID"), chi.URLParam(request, "artifactID"))
	if err != nil {
		s.writeStoreError(writer, "download artifact", err)
		return
	}
	workspacePath, err := filepath.EvalSymlinks(artifact.WorkspacePath)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, "artifact_unavailable", "artifact workspace is unavailable on the control plane")
		return
	}
	artifactPath, err := filepath.EvalSymlinks(artifact.HostPath)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, "artifact_unavailable", "artifact content is unavailable on the control plane")
		return
	}
	relative, err := filepath.Rel(workspacePath, artifactPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		writeProblem(writer, http.StatusForbidden, "forbidden", "artifact path escapes its workspace")
		return
	}
	file, err := os.Open(artifactPath)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, "artifact_unavailable", "artifact content is unavailable on the control plane")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeProblem(writer, http.StatusServiceUnavailable, "artifact_unavailable", "artifact content is not a regular file")
		return
	}
	filename := strings.ReplaceAll(strings.ReplaceAll(artifact.Name, "\"", ""), "\r", "")
	filename = strings.ReplaceAll(filename, "\n", "")
	writer.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	if artifact.MIMEType != "" {
		writer.Header().Set("Content-Type", artifact.MIMEType)
	}
	http.ServeContent(writer, request, artifact.Name, info.ModTime(), file)
}

func (s *Server) handleWorkspaceDiff(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	result, command, err := s.store.RequestWorkspaceDiff(request.Context(), user, chi.URLParam(request, "boxID"), request.URL.Query().Get("baseRef"), request.URL.Query().Get("headRef"))
	if err != nil {
		s.writeStoreError(writer, "request workspace diff", err)
		return
	}
	if command != nil {
		s.dispatch(command)
	}
	switch result.Status {
	case "pending":
		writeJSON(writer, http.StatusAccepted, result)
	case "failed":
		writeProblem(writer, http.StatusBadGateway, "diff_failed", result.Error)
	default:
		writeJSON(writer, http.StatusOK, result)
	}
}
