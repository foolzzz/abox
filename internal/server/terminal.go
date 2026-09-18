package server

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"sync"

	hostv1 "agentbox/api"
	"agentbox/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type terminalSubscription struct {
	hostID string
	data   chan *hostv1.TerminalData
}

type terminalHub struct {
	mu       sync.RWMutex
	sessions map[string]*terminalSubscription
}

func newTerminalHub() *terminalHub {
	return &terminalHub{sessions: make(map[string]*terminalSubscription)}
}

func (h *terminalHub) register(sessionID, hostID string) *terminalSubscription {
	subscription := &terminalSubscription{hostID: hostID, data: make(chan *hostv1.TerminalData, 256)}
	h.mu.Lock()
	h.sessions[sessionID] = subscription
	h.mu.Unlock()
	return subscription
}

func (h *terminalHub) unregister(sessionID string, subscription *terminalSubscription) {
	h.mu.Lock()
	if h.sessions[sessionID] == subscription {
		delete(h.sessions, sessionID)
		close(subscription.data)
	}
	h.mu.Unlock()
}

func (h *terminalHub) publish(data *hostv1.TerminalData) {
	if data == nil || data.GetSessionId() == "" {
		return
	}
	h.mu.RLock()
	subscription := h.sessions[data.GetSessionId()]
	if subscription != nil {
		select {
		case subscription.data <- data:
		default:
		}
	}
	h.mu.RUnlock()
}

type terminalClientMessage struct {
	Type    string `json:"type"`
	Data    string `json:"data,omitempty"`
	Columns uint32 `json:"columns,omitempty"`
	Rows    uint32 `json:"rows,omitempty"`
}

type terminalServerMessage struct {
	Type     string `json:"type"`
	Data     string `json:"data,omitempty"`
	Stream   string `json:"stream,omitempty"`
	Closed   bool   `json:"closed,omitempty"`
	ExitCode int32  `json:"exitCode,omitempty"`
	Error    string `json:"error,omitempty"`
}

var terminalUpgrader = websocket.Upgrader{
	ReadBufferSize:  16 << 10,
	WriteBufferSize: 16 << 10,
	CheckOrigin: func(request *http.Request) bool {
		origin := request.Header.Get("Origin")
		if origin == "" {
			return true
		}
		parsed, err := url.Parse(origin)
		return err == nil && strings.EqualFold(parsed.Host, request.Host)
	},
}

func (s *Server) terminateAgentTerminal(box domain.Box) {
	s.hosts.sendTerminal(box.HostID, &hostv1.TerminalInput{BoxId: box.ID, Terminate: true})
}

func (s *Server) handleCommandTerminal(writer http.ResponseWriter, request *http.Request) {
	s.handleTerminal(writer, request, "command")
}

func (s *Server) handleAgentTerminal(writer http.ResponseWriter, request *http.Request) {
	s.handleTerminal(writer, request, "agent")
}

func (s *Server) handleTerminal(writer http.ResponseWriter, request *http.Request, mode string) {
	user, ok := requestUser(request)
	if !ok {
		writeProblem(writer, http.StatusUnauthorized, "unauthenticated", "authentication is required")
		return
	}
	boxID := chi.URLParam(request, "boxID")
	if err := s.store.AuthorizeBoxOperation(request.Context(), user, boxID); err != nil {
		s.writeStoreError(writer, "authorize terminal", err)
		return
	}
	box, err := s.store.GetBox(request.Context(), user, boxID)
	if err != nil {
		s.writeStoreError(writer, "load terminal box", err)
		return
	}
	workspace, err := s.store.GetWorkspace(request.Context(), user, box.WorkspaceID)
	if err != nil {
		s.writeStoreError(writer, "load terminal workspace", err)
		return
	}
	connection, err := terminalUpgrader.Upgrade(writer, request, nil)
	if err != nil {
		return
	}
	defer func() { _ = connection.Close() }()

	sessionID := uuid.NewString()
	subscription := s.terminals.register(sessionID, box.HostID)
	defer s.terminals.unregister(sessionID, subscription)
	if !s.hosts.sendTerminal(box.HostID, &hostv1.TerminalInput{SessionId: sessionID, BoxId: box.ID, Workspace: workspace.Path, Mode: mode, RuntimeType: box.RuntimeType, Model: box.Model, Open: true, Columns: 120, Rows: 32}) {
		_ = connection.WriteJSON(terminalServerMessage{Type: "error", Error: "host is offline"})
		return
	}
	defer s.hosts.sendTerminal(box.HostID, &hostv1.TerminalInput{SessionId: sessionID, BoxId: box.ID, Close: true})

	clientMessages := make(chan terminalClientMessage, 16)
	readErrors := make(chan error, 1)
	go func() {
		for {
			var message terminalClientMessage
			if readErr := connection.ReadJSON(&message); readErr != nil {
				readErrors <- readErr
				return
			}
			clientMessages <- message
		}
	}()

	for {
		select {
		case <-request.Context().Done():
			return
		case <-readErrors:
			return
		case message := <-clientMessages:
			input := &hostv1.TerminalInput{SessionId: sessionID, BoxId: box.ID}
			switch message.Type {
			case "input":
				input.Data = []byte(message.Data)
			case "resize":
				input.Columns, input.Rows = message.Columns, message.Rows
			case "close":
				return
			default:
				_ = connection.WriteJSON(terminalServerMessage{Type: "error", Error: "unsupported terminal message"})
				continue
			}
			if !s.hosts.sendTerminal(box.HostID, input) {
				_ = connection.WriteJSON(terminalServerMessage{Type: "error", Error: "host disconnected"})
				return
			}
		case data, open := <-subscription.data:
			if !open {
				return
			}
			stream := "stdout"
			if data.GetStream() == hostv1.TerminalStream_TERMINAL_STREAM_STDERR {
				stream = "stderr"
			}
			message := terminalServerMessage{Type: "data", Data: base64.StdEncoding.EncodeToString(data.GetData()), Stream: stream, Closed: data.GetClosed(), ExitCode: data.GetExitCode(), Error: data.GetError()}
			if writeErr := connection.WriteJSON(message); writeErr != nil {
				return
			}
			if data.GetClosed() {
				return
			}
		}
	}
}
