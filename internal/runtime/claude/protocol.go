package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"agentbox/internal/events"
)

type userInputMessage struct {
	Type    string      `json:"type"`
	Message userMessage `json:"message"`
}

type userMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type controlRequest struct {
	Type      string             `json:"type"`
	RequestID string             `json:"request_id"`
	Request   controlRequestBody `json:"request"`
}

type controlRequestBody struct {
	Subtype string `json:"subtype"`
	Mode    string `json:"mode,omitempty"`
}

type envelope struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	SessionID       string          `json:"session_id"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	UUID            string          `json:"uuid"`
	IsReplay        bool            `json:"isReplay"`
	IsReplayLegacy  bool            `json:"is_replay"`
	IsError         bool            `json:"is_error"`
	Message         json.RawMessage `json:"message"`
	Event           json.RawMessage `json:"event"`
	Request         json.RawMessage `json:"request"`
	RequestID       string          `json:"request_id"`
	Response        json.RawMessage `json:"response"`
	Result          json.RawMessage `json:"result"`
	DurationMS      int64           `json:"duration_ms"`
	DurationAPIMS   int64           `json:"duration_api_ms"`
	NumTurns        int             `json:"num_turns"`
	TotalCostUSD    float64         `json:"total_cost_usd"`
	Usage           json.RawMessage `json:"usage"`
	ModelUsage      json.RawMessage `json:"modelUsage"`
	StopReason      string          `json:"stop_reason"`
	Raw             json.RawMessage `json:"-"`
}

type systemInit struct {
	CWD               string          `json:"cwd"`
	SessionID         string          `json:"session_id"`
	Tools             json.RawMessage `json:"tools"`
	MCPServers        json.RawMessage `json:"mcp_servers"`
	Model             string          `json:"model"`
	PermissionMode    string          `json:"permissionMode"`
	ClaudeCodeVersion string          `json:"claude_code_version"`
}

type protocolMessage struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type partialEvent struct {
	Type         string          `json:"type"`
	Index        int             `json:"index"`
	Message      json.RawMessage `json:"message"`
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        json.RawMessage `json:"delta"`
}

type partialDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Thinking    string `json:"thinking"`
	PartialJSON string `json:"partial_json"`
}

type incomingControlRequest struct {
	Subtype               string          `json:"subtype"`
	ToolName              string          `json:"tool_name"`
	Input                 json.RawMessage `json:"input"`
	PermissionSuggestions json.RawMessage `json:"permission_suggestions"`
	BlockedPath           string          `json:"blocked_path"`
}

type messageState struct {
	id      string
	partial bool
}

type blockState struct {
	toolID   string
	toolName string
}

type toolState struct {
	id        string
	name      string
	input     json.RawMessage
	parentID  string
	completed bool
}

type subagentState struct {
	id        string
	completed bool
}

type turnParser struct {
	messages       map[string]*messageState
	currentByActor map[string]string
	blocks         map[string]blockState
	tools          map[string]*toolState
	toolOrder      []string
	subagents      map[string]*subagentState
	subagentOrder  []string
}

func newTurnParser() *turnParser {
	return &turnParser{
		messages:       make(map[string]*messageState),
		currentByActor: make(map[string]string),
		blocks:         make(map[string]blockState),
		tools:          make(map[string]*toolState),
		subagents:      make(map[string]*subagentState),
	}
}

func (h *processHandle) readOutput() {
	defer close(h.readerDone)
	err := scanJSONLines(h.stdout, h.adapter.maxMessageBytes, func(line uint64, data []byte) error {
		return h.processLine(line, data)
	})
	if err == nil {
		return
	}

	protocolErr, ok := err.(*ProtocolError)
	if !ok {
		protocolErr = &ProtocolError{Kind: "read_failed", Detail: err.Error(), Cause: err}
	}
	h.failProtocol(protocolErr)
}

func scanJSONLines(reader io.Reader, maxBytes int, consume func(uint64, []byte) error) error {
	bufferSize := 64 << 10
	if maxBytes < bufferSize {
		bufferSize = maxBytes
	}
	if bufferSize < 1 {
		bufferSize = 1
	}
	readerBuffer := bufio.NewReaderSize(reader, bufferSize)
	lineBuffer := make([]byte, 0, bufferSize)
	var lineNumber uint64

	for {
		fragment, prefix, err := readerBuffer.ReadLine()
		if len(lineBuffer)+len(fragment) > maxBytes {
			return &ProtocolError{
				Line:   lineNumber + 1,
				Kind:   "message_too_large",
				Detail: fmt.Sprintf("line exceeds %d-byte limit", maxBytes),
			}
		}
		lineBuffer = append(lineBuffer, fragment...)

		if prefix {
			if err != nil {
				return &ProtocolError{Line: lineNumber + 1, Kind: "read_failed", Detail: err.Error(), Cause: err}
			}
			continue
		}

		if err == io.EOF && len(lineBuffer) == 0 {
			return nil
		}
		lineNumber++
		if len(bytes.TrimSpace(lineBuffer)) != 0 {
			if consumeErr := consume(lineNumber, lineBuffer); consumeErr != nil {
				return consumeErr
			}
		}
		lineBuffer = lineBuffer[:0]

		if err != nil {
			if err == io.EOF {
				return nil
			}
			return &ProtocolError{Line: lineNumber + 1, Kind: "read_failed", Detail: err.Error(), Cause: err}
		}
	}
}

func (h *processHandle) processLine(lineNumber uint64, data []byte) error {
	var message envelope
	if err := json.Unmarshal(data, &message); err != nil {
		return &ProtocolError{
			Line:   lineNumber,
			Kind:   "malformed_json",
			Detail: err.Error(),
			Cause:  err,
		}
	}
	if message.Type == "" {
		return &ProtocolError{Line: lineNumber, Kind: "missing_message_type", Detail: boundedPreview(data, 256)}
	}
	message.Raw = append(json.RawMessage(nil), data...)
	if message.SessionID != "" {
		h.captureSession(message.SessionID)
	}

	if h.dropUntilReplay(&message) {
		return nil
	}

	switch message.Type {
	case "system":
		return h.handleSystem(lineNumber, &message)
	case "user":
		return h.handleUser(lineNumber, &message)
	case "assistant":
		return h.handleAssistant(lineNumber, &message)
	case "stream_event":
		return h.handleStreamEvent(lineNumber, &message)
	case "result":
		return h.handleResult(lineNumber, &message)
	case "control_request":
		return h.handleControlRequest(lineNumber, &message)
	case "control_response":
		h.emitForRun(h.currentRunID(), events.Notice, "system", "", map[string]any{
			"kind":      "control.response",
			"requestId": message.RequestID,
			"response":  rawOrNull(message.Response),
		})
		return nil
	case "rate_limit_event", "prompt_suggestion":
		h.emitForRun(h.currentRunID(), events.Notice, "system", "", map[string]any{
			"kind":   message.Type,
			"detail": boundedPreview(message.Raw, 4096),
		})
		return nil
	default:
		h.emitForRun(h.currentRunID(), events.Notice, "system", "", map[string]any{
			"kind":        "protocol.unknown_message",
			"messageType": message.Type,
			"detail":      boundedPreview(message.Raw, 4096),
		})
		return nil
	}
}

func (h *processHandle) captureSession(sessionID string) {
	h.mu.Lock()
	h.sessionRef = sessionID
	h.mu.Unlock()
}

func (h *processHandle) dropUntilReplay(message *envelope) bool {
	h.mu.Lock()
	current := h.current
	if current == nil || !current.awaitingReplay {
		h.mu.Unlock()
		return false
	}
	if message.Type == "user" && message.replayed() && h.matchesReplay(current, message) {
		current.awaitingReplay = false
		current.residualRunID = ""
		h.mu.Unlock()
		return false
	}
	h.mu.Unlock()

	runID := current.residualRunID
	if runID == "" {
		runID = current.runID
	}
	h.emitForRun(runID, events.Notice, "daemon", "", map[string]any{
		"kind":        "protocol.residual_dropped",
		"messageType": message.Type,
		"uuid":        message.UUID,
		"reason":      "waiting for replay of the queued turn after an interrupt boundary",
	})
	return true
}

func (h *processHandle) matchesReplay(t *turn, message *envelope) bool {
	var replay protocolMessage
	if json.Unmarshal(message.Message, &replay) != nil {
		return false
	}
	var text string
	if json.Unmarshal(replay.Content, &text) == nil {
		return text == t.message
	}
	return false
}

func (message *envelope) replayed() bool {
	return message.IsReplay || message.IsReplayLegacy
}

func (h *processHandle) handleSystem(lineNumber uint64, message *envelope) error {
	if message.Subtype != "init" {
		h.emitForRun(h.currentRunID(), events.Notice, "system", "", map[string]any{
			"kind":   "system." + message.Subtype,
			"detail": boundedPreview(message.Raw, 4096),
			"line":   lineNumber,
		})
		return nil
	}

	var init systemInit
	if err := json.Unmarshal(message.Raw, &init); err != nil {
		return &ProtocolError{Line: lineNumber, Kind: "malformed_system_init", Detail: err.Error(), Cause: err}
	}
	if init.SessionID != "" {
		h.captureSession(init.SessionID)
	}

	h.mu.Lock()
	if h.current == nil && h.status != "stopping" && h.status != "exited" {
		h.status = "ready"
	}
	h.mu.Unlock()
	h.emit(events.RuntimeReady, "system", "", map[string]any{
		"sessionId":         h.SessionRef(),
		"cwd":               init.CWD,
		"model":             init.Model,
		"claudeCodeVersion": init.ClaudeCodeVersion,
		"permissionMode":    init.PermissionMode,
		"tools":             rawOrNull(init.Tools),
		"mcpServers":        rawOrNull(init.MCPServers),
	})
	return nil
}

func (h *processHandle) handleUser(lineNumber uint64, message *envelope) error {
	var user protocolMessage
	if len(message.Message) == 0 {
		return &ProtocolError{Line: lineNumber, Kind: "missing_user_message"}
	}
	if err := json.Unmarshal(message.Message, &user); err != nil {
		return &ProtocolError{Line: lineNumber, Kind: "malformed_user_message", Detail: err.Error(), Cause: err}
	}
	if message.replayed() {
		return nil
	}

	t := h.currentTurn()
	if t == nil {
		return nil
	}
	parentID := dereference(message.ParentToolUseID)
	blocks, text, err := decodeContent(user.Content)
	if err != nil {
		return &ProtocolError{Line: lineNumber, Kind: "malformed_user_content", Detail: err.Error(), Cause: err}
	}

	if parentID != "" {
		t.parser.ensureSubagent(h, t, parentID, "forwarded_user_message")
	}
	if text != "" && parentID != "" {
		h.emitTurn(t, events.MessageStarted, parentID, map[string]any{"messageId": user.ID, "role": user.Role})
		h.emitTurn(t, events.MessageDelta, parentID, map[string]any{"messageId": user.ID, "text": text})
		h.emitTurn(t, events.SubagentProgress, parentID, map[string]any{"messageId": user.ID, "text": text})
		h.emitTurn(t, events.MessageCompleted, parentID, map[string]any{"messageId": user.ID, "role": user.Role})
	}
	for _, block := range blocks {
		if block.Type == "tool_result" {
			t.parser.completeTool(h, t, parentID, block)
		}
	}
	return nil
}

func (h *processHandle) handleAssistant(lineNumber uint64, message *envelope) error {
	var assistant protocolMessage
	if len(message.Message) == 0 {
		return &ProtocolError{Line: lineNumber, Kind: "missing_assistant_message"}
	}
	if err := json.Unmarshal(message.Message, &assistant); err != nil {
		return &ProtocolError{Line: lineNumber, Kind: "malformed_assistant_message", Detail: err.Error(), Cause: err}
	}
	blocks, text, err := decodeContent(assistant.Content)
	if err != nil {
		return &ProtocolError{Line: lineNumber, Kind: "malformed_assistant_content", Detail: err.Error(), Cause: err}
	}

	t := h.currentTurn()
	if t == nil {
		return nil
	}
	parentID := dereference(message.ParentToolUseID)
	if parentID != "" {
		t.parser.ensureSubagent(h, t, parentID, "forwarded_assistant_message")
	}
	state := t.parser.messages[assistant.ID]
	partial := state != nil && state.partial
	if !partial {
		h.emitTurn(t, events.MessageStarted, parentID, map[string]any{
			"messageId": assistant.ID,
			"model":     assistant.Model,
			"role":      assistant.Role,
		})
		if text != "" {
			h.emitTurn(t, events.MessageDelta, parentID, map[string]any{"messageId": assistant.ID, "text": text})
			if parentID != "" {
				h.emitTurn(t, events.SubagentProgress, parentID, map[string]any{"messageId": assistant.ID, "text": text})
			}
		}
	}
	for _, block := range blocks {
		switch block.Type {
		case "tool_use", "server_tool_use":
			t.parser.startTool(h, t, parentID, block)
		case "text":
			// Text was emitted from the aggregate content above.
		case "thinking":
			if !partial && block.Thinking != "" {
				h.emitTurn(t, events.MessageDelta, parentID, map[string]any{
					"messageId": assistant.ID,
					"text":      block.Thinking,
					"channel":   "thinking",
				})
			}
		}
	}
	if !partial {
		h.emitTurn(t, events.MessageCompleted, parentID, map[string]any{
			"messageId": assistant.ID,
			"role":      assistant.Role,
		})
	}
	return nil
}

func (h *processHandle) handleStreamEvent(lineNumber uint64, message *envelope) error {
	var stream partialEvent
	if len(message.Event) == 0 {
		return &ProtocolError{Line: lineNumber, Kind: "missing_stream_event"}
	}
	if err := json.Unmarshal(message.Event, &stream); err != nil {
		return &ProtocolError{Line: lineNumber, Kind: "malformed_stream_event", Detail: err.Error(), Cause: err}
	}

	t := h.currentTurn()
	if t == nil {
		return nil
	}
	parentID := dereference(message.ParentToolUseID)
	if parentID != "" {
		t.parser.ensureSubagent(h, t, parentID, "forwarded_stream_event")
	}
	actorKey := parentID

	switch stream.Type {
	case "message_start":
		var started protocolMessage
		if err := json.Unmarshal(stream.Message, &started); err != nil {
			return &ProtocolError{Line: lineNumber, Kind: "malformed_message_start", Detail: err.Error(), Cause: err}
		}
		t.parser.messages[started.ID] = &messageState{id: started.ID, partial: true}
		t.parser.currentByActor[actorKey] = started.ID
		h.emitTurn(t, events.MessageStarted, parentID, map[string]any{
			"messageId": started.ID,
			"model":     started.Model,
			"role":      started.Role,
		})
	case "content_block_start":
		var block contentBlock
		if err := json.Unmarshal(stream.ContentBlock, &block); err != nil {
			return &ProtocolError{Line: lineNumber, Kind: "malformed_content_block_start", Detail: err.Error(), Cause: err}
		}
		if block.Type == "tool_use" || block.Type == "server_tool_use" {
			t.parser.blocks[blockKey(actorKey, stream.Index)] = blockState{toolID: block.ID, toolName: block.Name}
			t.parser.startTool(h, t, parentID, block)
		}
	case "content_block_delta":
		var delta partialDelta
		if err := json.Unmarshal(stream.Delta, &delta); err != nil {
			return &ProtocolError{Line: lineNumber, Kind: "malformed_content_block_delta", Detail: err.Error(), Cause: err}
		}
		messageID := t.parser.currentByActor[actorKey]
		switch delta.Type {
		case "text_delta":
			h.emitTurn(t, events.MessageDelta, parentID, map[string]any{"messageId": messageID, "text": delta.Text})
			if parentID != "" {
				h.emitTurn(t, events.SubagentProgress, parentID, map[string]any{"messageId": messageID, "text": delta.Text})
			}
		case "thinking_delta":
			h.emitTurn(t, events.MessageDelta, parentID, map[string]any{
				"messageId": messageID,
				"text":      delta.Thinking,
				"channel":   "thinking",
			})
		case "input_json_delta":
			block := t.parser.blocks[blockKey(actorKey, stream.Index)]
			h.emitTurn(t, events.ToolProgress, parentID, map[string]any{
				"toolUseId":   block.toolID,
				"tool":        block.toolName,
				"partialJson": delta.PartialJSON,
			})
		}
	case "content_block_stop":
		delete(t.parser.blocks, blockKey(actorKey, stream.Index))
	case "message_stop":
		messageID := t.parser.currentByActor[actorKey]
		h.emitTurn(t, events.MessageCompleted, parentID, map[string]any{"messageId": messageID})
		delete(t.parser.currentByActor, actorKey)
	case "message_delta":
		// The final usage and stop reason are represented by message_stop and result.
	default:
		h.emitForRun(t.runID, events.Notice, "system", "", map[string]any{
			"kind":      "protocol.unknown_stream_event",
			"eventType": stream.Type,
		})
	}
	return nil
}

func (h *processHandle) handleResult(_ uint64, message *envelope) error {
	h.mu.Lock()
	t := h.current
	if t == nil {
		runID := h.guardRunID
		if runID == "" {
			runID = h.spec.RunID
		}
		h.mu.Unlock()
		h.emitForRun(runID, events.Notice, "daemon", "", map[string]any{
			"kind":    "protocol.orphan_result",
			"subtype": message.Subtype,
			"uuid":    message.UUID,
		})
		return nil
	}
	interrupted := t.interrupted
	promoted := h.pending
	h.pending = nil
	if promoted != nil {
		h.current = promoted
		h.status = "busy"
		h.guardNextReplay = false
		h.guardRunID = ""
	} else {
		h.current = nil
		if interrupted {
			h.guardNextReplay = true
			h.guardRunID = t.runID
		}
		if h.status != "stopping" && h.status != "exited" {
			h.status = "ready"
		}
	}
	h.mu.Unlock()

	failed := interrupted || message.IsError || (message.Subtype != "" && message.Subtype != "success")
	t.parser.finishSubagents(h, t, interrupted)
	t.parser.finishTools(h, t, failed)
	payload := map[string]any{
		"inputId":       t.inputID,
		"turnNumber":    t.sequence,
		"subtype":       message.Subtype,
		"result":        rawOrNull(message.Result),
		"stopReason":    message.StopReason,
		"durationMs":    message.DurationMS,
		"durationApiMs": message.DurationAPIMS,
		"numTurns":      message.NumTurns,
		"totalCostUsd":  message.TotalCostUSD,
		"usage":         rawOrNull(message.Usage),
		"modelUsage":    rawOrNull(message.ModelUsage),
	}
	if interrupted {
		payload["reason"] = "interrupted"
		payload["interrupted"] = true
		h.emitForRun(t.runID, events.RunFailed, "daemon", "", payload)
	} else if failed {
		payload["reason"] = "claude_result_error"
		h.emitForRun(t.runID, events.RunFailed, "daemon", "", payload)
	} else {
		h.emitForRun(t.runID, events.RunCompleted, "daemon", "", payload)
	}

	if promoted != nil {
		if err := h.startTurn(context.Background(), promoted, true); err != nil {
			return nil
		}
	}
	return nil
}

func (h *processHandle) handleControlRequest(lineNumber uint64, message *envelope) error {
	var request incomingControlRequest
	if err := json.Unmarshal(message.Request, &request); err != nil {
		return &ProtocolError{Line: lineNumber, Kind: "malformed_control_request", Detail: err.Error(), Cause: err}
	}
	h.emitForRun(h.currentRunID(), events.ApprovalRequested, "system", "", map[string]any{
		"approvalId":            message.RequestID,
		"tool":                  request.ToolName,
		"input":                 rawOrNull(request.Input),
		"permissionSuggestions": rawOrNull(request.PermissionSuggestions),
		"blockedPath":           request.BlockedPath,
		"defaultDecision":       "deny",
		"supported":             false,
	})
	return &ProtocolError{
		Line:   lineNumber,
		Kind:   "interactive_approval_unsupported",
		Detail: fmt.Sprintf("control request %q (%s) cannot be answered in static-policy mode", message.RequestID, request.Subtype),
		Cause:  ErrInteractiveApprovalUnsupported,
	}
}

func (h *processHandle) currentTurn() *turn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.current
}

func (h *processHandle) currentRunID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current != nil {
		return h.current.runID
	}
	if h.guardRunID != "" {
		return h.guardRunID
	}
	return h.spec.RunID
}

func (h *processHandle) failProtocol(err error) {
	h.mu.Lock()
	if h.protocolErr != nil {
		h.mu.Unlock()
		return
	}
	h.protocolErr = err
	current := h.current
	pending := h.pending
	noticeRunID := h.spec.RunID
	if current != nil {
		noticeRunID = current.runID
		if current.awaitingReplay && current.residualRunID != "" {
			noticeRunID = current.residualRunID
		}
	} else if pending != nil {
		noticeRunID = pending.runID
	}
	h.current = nil
	h.pending = nil
	h.mu.Unlock()

	_ = signalProcessGroup(h.cmd.Process, osKillSignal())
	if current != nil {
		h.emitForRun(current.runID, events.RunFailed, "daemon", "", map[string]any{
			"inputId":    current.inputID,
			"turnNumber": current.sequence,
			"reason":     "protocol_error",
			"error":      err.Error(),
		})
	}
	if pending != nil {
		h.emitForRun(pending.runID, events.RunFailed, "daemon", "", map[string]any{
			"inputId":    pending.inputID,
			"turnNumber": pending.sequence,
			"reason":     "protocol_error",
			"error":      err.Error(),
		})
	}
	h.emitForRun(noticeRunID, events.Notice, "system", "", map[string]any{
		"kind":  "protocol.error",
		"error": err.Error(),
	})
}

func (p *turnParser) startTool(h *processHandle, t *turn, parentID string, block contentBlock) {
	if block.ID == "" {
		return
	}
	state := p.tools[block.ID]
	if state == nil {
		state = &toolState{id: block.ID, name: block.Name, input: cloneRaw(block.Input), parentID: parentID}
		p.tools[block.ID] = state
		p.toolOrder = append(p.toolOrder, block.ID)
		h.emitTurn(t, events.ToolStarted, parentID, map[string]any{
			"toolUseId": block.ID,
			"tool":      block.Name,
			"input":     rawOrObject(block.Input),
		})
	} else {
		if block.Name != "" {
			state.name = block.Name
		}
		if len(block.Input) != 0 {
			state.input = cloneRaw(block.Input)
		}
	}

	if isSubagentTool(state.name) {
		p.ensureSubagent(h, t, block.ID, "tool_use")
	}
	if strings.EqualFold(state.name, "TodoWrite") && len(block.Input) != 0 {
		todos := todoPayload(block.Input)
		h.emitTurn(t, events.TodoUpdated, parentID, map[string]any{
			"toolUseId": block.ID,
			"todos":     todos,
		})
	}
}

func (p *turnParser) completeTool(h *processHandle, t *turn, parentID string, block contentBlock) {
	if block.ToolUseID == "" {
		return
	}
	state := p.tools[block.ToolUseID]
	if state == nil {
		state = &toolState{id: block.ToolUseID, parentID: parentID}
		p.tools[block.ToolUseID] = state
	}
	if state.completed {
		return
	}
	state.completed = true

	if subagent := p.subagents[block.ToolUseID]; subagent != nil && !subagent.completed {
		subagent.completed = true
		eventType := events.SubagentCompleted
		payload := map[string]any{
			"parentToolUseId": block.ToolUseID,
			"result":          rawOrNull(block.Content),
		}
		if block.IsError {
			payload["failed"] = true
		}
		h.emitTurn(t, eventType, block.ToolUseID, payload)
	}

	eventType := events.ToolCompleted
	if block.IsError {
		eventType = events.ToolFailed
	}
	h.emitTurn(t, eventType, state.parentID, map[string]any{
		"toolUseId": block.ToolUseID,
		"tool":      state.name,
		"result":    rawOrNull(block.Content),
		"isError":   block.IsError,
	})
}

func (p *turnParser) ensureSubagent(h *processHandle, t *turn, parentID, source string) {
	if parentID == "" {
		return
	}
	if _, exists := p.subagents[parentID]; exists {
		return
	}
	p.subagents[parentID] = &subagentState{id: parentID}
	p.subagentOrder = append(p.subagentOrder, parentID)
	tool := p.tools[parentID]
	payload := map[string]any{
		"parentToolUseId": parentID,
		"source":          source,
	}
	if tool != nil {
		payload["tool"] = tool.name
		payload["input"] = rawOrObject(tool.input)
		if tool.parentID != "" {
			payload["parentSubagentId"] = tool.parentID
		}
	}
	h.emitTurn(t, events.SubagentStarted, parentID, payload)
}

func (p *turnParser) finishSubagents(h *processHandle, t *turn, interrupted bool) {
	for _, subagentID := range p.subagentOrder {
		subagent := p.subagents[subagentID]
		if subagent.completed {
			continue
		}
		subagent.completed = true
		h.emitTurn(t, events.SubagentCompleted, subagent.id, map[string]any{
			"parentToolUseId": subagent.id,
			"interrupted":     interrupted,
			"reason":          "turn_boundary",
		})
	}
}

func (p *turnParser) finishTools(h *processHandle, t *turn, failed bool) {
	for _, toolID := range p.toolOrder {
		tool := p.tools[toolID]
		if tool.completed {
			continue
		}
		tool.completed = true
		eventType := events.ToolCompleted
		if failed {
			eventType = events.ToolFailed
		}
		h.emitTurn(t, eventType, tool.parentID, map[string]any{
			"toolUseId": tool.id,
			"tool":      tool.name,
			"reason":    "turn_boundary",
			"failed":    failed,
		})
	}
}

func (h *processHandle) emitTurn(t *turn, eventType, parentID string, payload map[string]any) {
	payload["inputId"] = t.inputID
	payload["turnNumber"] = t.sequence
	if parentID == "" {
		h.emitForRun(t.runID, eventType, "main_agent", "", payload)
		return
	}
	payload["parentToolUseId"] = parentID
	h.emitForRun(t.runID, eventType, "subagent", parentID, payload)
}

func decodeContent(raw json.RawMessage) ([]contentBlock, string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return nil, text, nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, "", err
	}
	var combined strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			combined.WriteString(block.Text)
		}
	}
	return blocks, combined.String(), nil
}

func blockKey(actor string, index int) string {
	return fmt.Sprintf("%s\x00%d", actor, index)
}

func isSubagentTool(name string) bool {
	return strings.EqualFold(name, "Agent") || strings.EqualFold(name, "Task")
}

func dereference(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func rawOrNull(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

func rawOrObject(raw json.RawMessage) any {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func todoPayload(raw json.RawMessage) any {
	var input struct {
		Todos json.RawMessage `json:"todos"`
	}
	if json.Unmarshal(raw, &input) == nil && len(input.Todos) != 0 {
		return input.Todos
	}
	return rawOrObject(raw)
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}

func boundedPreview(data []byte, limit int) string {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) <= limit {
		return string(trimmed)
	}
	return string(trimmed[:limit]) + "..."
}
