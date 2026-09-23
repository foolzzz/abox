package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	hostv1 "agentbox/api"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type runtimeSessionHub struct {
	mu      sync.Mutex
	pending map[string]chan *hostv1.RuntimeSessionList
}

func newRuntimeSessionHub() *runtimeSessionHub {
	return &runtimeSessionHub{pending: make(map[string]chan *hostv1.RuntimeSessionList)}
}

func (h *runtimeSessionHub) wait(ctx context.Context, requestID string, send func() bool) (*hostv1.RuntimeSessionList, error) {
	response := make(chan *hostv1.RuntimeSessionList, 1)
	h.mu.Lock()
	h.pending[requestID] = response
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.pending, requestID)
		h.mu.Unlock()
	}()
	if !send() {
		return nil, errors.New("host is offline")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-response:
		return result, nil
	}
}

func (h *runtimeSessionHub) publish(result *hostv1.RuntimeSessionList) {
	if result == nil || result.GetRequestId() == "" {
		return
	}
	h.mu.Lock()
	response := h.pending[result.GetRequestId()]
	h.mu.Unlock()
	if response == nil {
		return
	}
	select {
	case response <- result:
	default:
	}
}

func (s *Server) handleListRuntimeSessions(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	hostID := chi.URLParam(request, "hostID")
	if _, err := s.store.GetHost(request.Context(), user, hostID); err != nil {
		s.writeStoreError(writer, "load session host", err)
		return
	}
	runtimeType := strings.TrimSpace(request.URL.Query().Get("runtime"))
	if runtimeType == "" {
		runtimeType = "claude"
	}
	requestID := uuid.NewString()
	ctx, cancel := context.WithTimeout(request.Context(), 10*time.Second)
	defer cancel()
	result, err := s.runtimeSessions.wait(ctx, requestID, func() bool {
		return s.hosts.sendRuntimeSessionQuery(hostID, &hostv1.RuntimeSessionQuery{RequestId: requestID, RuntimeType: runtimeType})
	})
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, "session_discovery_unavailable", err.Error())
		return
	}
	if result.GetError() != "" {
		writeProblem(writer, http.StatusBadGateway, "session_discovery_failed", result.GetError())
		return
	}
	type responseSession struct {
		SessionRef  string    `json:"sessionRef"`
		RuntimeType string    `json:"runtimeType"`
		Workspace   string    `json:"workspace"`
		Name        string    `json:"name"`
		Status      string    `json:"status"`
		Running     bool      `json:"running"`
		UpdatedAt   time.Time `json:"updatedAt"`
	}
	sessions := make([]responseSession, 0, len(result.GetSessions()))
	for _, session := range result.GetSessions() {
		if session == nil {
			continue
		}
		sessions = append(sessions, responseSession{
			SessionRef: session.GetSessionRef(), RuntimeType: session.GetRuntimeType(), Workspace: session.GetWorkspace(),
			Name: session.GetName(), Status: session.GetStatus(), Running: session.GetRunning(),
			UpdatedAt: time.UnixMilli(session.GetUpdatedUnixMillis()).UTC(),
		})
	}
	writeJSON(writer, http.StatusOK, sessions)
}

func (s *Server) handleListRuntimeSessionAttachments(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	attachments, err := s.store.ListRuntimeSessionAttachments(request.Context(), user, chi.URLParam(request, "boxID"))
	if err != nil {
		s.writeStoreError(writer, "list runtime session attachments", err)
		return
	}
	writeJSON(writer, http.StatusOK, attachments)
}

func (s *Server) handleDetachRuntimeSession(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	boxID := chi.URLParam(request, "boxID")
	box, err := s.store.GetBox(request.Context(), user, boxID)
	if err != nil {
		s.writeStoreError(writer, "load detached session box", err)
		return
	}
	if err := s.store.DetachRuntimeSession(request.Context(), user, boxID); err != nil {
		s.writeStoreError(writer, "detach runtime session", err)
		return
	}
	s.terminateAgentTerminal(box)
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStopRuntimeSession(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	boxID := chi.URLParam(request, "boxID")
	box, err := s.store.GetBox(request.Context(), user, boxID)
	if err != nil {
		s.writeStoreError(writer, "load stopped session box", err)
		return
	}
	if strings.TrimSpace(box.RuntimeSessionRef) == "" {
		writeProblem(writer, http.StatusConflict, "runtime_session_missing", "box has no external runtime session")
		return
	}
	requestID := uuid.NewString()
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	defer cancel()
	result, err := s.runtimeSessions.wait(ctx, requestID, func() bool {
		return s.hosts.sendRuntimeSessionQuery(box.HostID, &hostv1.RuntimeSessionQuery{RequestId: requestID, RuntimeType: box.RuntimeType, Action: "stop", SessionRef: box.RuntimeSessionRef})
	})
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, "runtime_session_stop_unavailable", err.Error())
		return
	}
	if result.GetError() != "" {
		writeProblem(writer, http.StatusBadGateway, "runtime_session_stop_failed", result.GetError())
		return
	}
	boxes, err := s.store.StopRuntimeSession(request.Context(), user, boxID)
	if err != nil {
		s.writeStoreError(writer, "persist stopped runtime session", err)
		return
	}
	for _, attachedBox := range boxes {
		s.terminateAgentTerminal(attachedBox)
	}
	writer.WriteHeader(http.StatusNoContent)
}
