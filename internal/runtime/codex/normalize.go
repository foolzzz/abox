package codex

import (
	"encoding/json"
	"fmt"
	"strings"

	"agentbox/internal/events"
)

func (h *processHandle) normalizeNotification(method string, raw json.RawMessage) {
	fields := rawObject(raw)
	switch method {
	case "thread/started":
		thread := rawObject(fields["thread"])
		if threadID := rawString(thread["id"]); threadID != "" {
			h.setThread(threadID)
		}
	case "turn/started":
		h.handleTurnStarted(raw, fields)
	case "turn/completed":
		h.handleTurnCompleted(raw, fields)
	case "item/started":
		h.handleItem(raw, fields, true)
	case "item/completed":
		h.handleItem(raw, fields, false)
	case "item/agentMessage/delta":
		h.emit(events.MessageDelta, "main_agent", "main", map[string]any{
			"runtime":   "codex",
			"messageId": rawString(fields["itemId"]),
			"delta":     rawString(fields["delta"]),
			"turnId":    rawString(fields["turnId"]),
		})
	case "item/commandExecution/outputDelta", "item/fileChange/outputDelta", "item/fileChange/patchUpdated", "item/mcpToolCall/progress":
		h.emit(events.ToolProgress, "main_agent", "main", normalizedPayload(raw, fields))
	case "turn/plan/updated":
		h.handlePlanUpdate(raw, fields)
	case "error":
		h.handleRuntimeError(fields)
	case "thread/status/changed":
		h.handleThreadStatus(raw, fields)
	case "item/plan/delta", "item/reasoning/summaryTextDelta", "item/reasoning/summaryPartAdded", "item/reasoning/textDelta",
		"thread/tokenUsage/updated", "turn/diff/updated", "rawResponseItem/completed", "rawResponse/completed",
		"remoteControl/status/changed", "account/updated", "account/rateLimits/updated", "mcpServer/status/updated":
		// High-volume or connection-level notifications do not add a stable AgentBox projection.
	default:
		// New app-server notifications are intentionally ignored until they have a stable mapping.
	}
}

func (h *processHandle) handleTurnStarted(raw json.RawMessage, fields map[string]json.RawMessage) {
	turn := rawObject(fields["turn"])
	turnID := rawString(turn["id"])
	h.stateMu.RLock()
	alreadyFinished := turnID != "" && h.lastFinishedTurnID == turnID
	h.stateMu.RUnlock()
	if alreadyFinished {
		return
	}
	runID := h.currentRun()
	if turnID != "" {
		h.activateTurn(turnID, runID)
	}
	h.emitForRun(runID, events.RunStarted, "main_agent", "main", map[string]any{
		"runtime":      "codex",
		"turnId":       turnID,
		"runtimeEvent": raw,
	})
}

func (h *processHandle) handleTurnCompleted(raw json.RawMessage, fields map[string]json.RawMessage) {
	turn := rawObject(fields["turn"])
	turnID := rawString(turn["id"])
	status := rawString(turn["status"])
	runID := h.currentRun()

	h.stateMu.Lock()
	if turnID != "" && h.lastFinishedTurnID == turnID {
		h.stateMu.Unlock()
		return
	}
	h.lastFinishedTurnID = turnID
	h.turnFailed = false
	h.turnInterrupted = false
	h.turnError = ""
	if turnID == "" || h.activeTurnID == "" || h.activeTurnID == turnID {
		h.activeTurnID = ""
		h.currentRunID = ""
		if h.status != "stopping" && h.status != "exited" {
			h.status = "ready"
		}
	}
	h.stateMu.Unlock()

	payload := normalizedPayload(raw, fields)
	payload["turnId"] = turnID
	payload["status"] = status
	if turnError := rawObject(turn["error"]); turnError != nil {
		payload["error"] = rawString(turnError["message"])
	}
	if status == "completed" {
		h.emitForRun(runID, events.RunCompleted, "main_agent", "main", payload)
		return
	}
	payload["reason"] = firstNonEmpty(status, "turn_failed")
	h.emitForRun(runID, events.RunFailed, "main_agent", "main", payload)
}

func (h *processHandle) handleItem(raw json.RawMessage, fields map[string]json.RawMessage, started bool) {
	item := rawObject(fields["item"])
	itemType := rawString(item["type"])
	if itemType == "" {
		return
	}
	payload := normalizedPayload(raw, fields)
	for _, key := range []string{"id", "type", "text", "status", "command", "cwd", "exitCode", "durationMs", "query", "path"} {
		if value, ok := item[key]; ok {
			payload[key] = decodeRaw(value)
		}
	}
	payload["turnId"] = rawString(fields["turnId"])

	switch itemType {
	case "userMessage":
		if !started {
			h.emit(events.MessageCompleted, "user", "", payload)
		}
	case "agentMessage":
		if started {
			h.emit(events.MessageStarted, "main_agent", "main", payload)
		} else {
			h.emit(events.MessageCompleted, "main_agent", "main", payload)
		}
	case "plan", "reasoning", "contextCompaction", "hookPrompt":
		return
	case "collabAgentToolCall":
		h.handleCollabItem(payload, item, started)
	case "subAgentActivity":
		h.handleSubagentActivity(payload, item)
	default:
		if started {
			h.emit(events.ToolStarted, "main_agent", "main", payload)
		} else if itemFailed(item) {
			h.emit(events.ToolFailed, "main_agent", "main", payload)
		} else {
			h.emit(events.ToolCompleted, "main_agent", "main", payload)
		}
	}
}

func (h *processHandle) handleCollabItem(payload map[string]any, item map[string]json.RawMessage, started bool) {
	tool := rawString(item["tool"])
	var receivers []string
	_ = json.Unmarshal(item["receiverThreadIds"], &receivers)
	agentStates := rawObject(item["agentsStates"])
	for _, receiver := range receivers {
		if receiver == "" {
			continue
		}
		state := rawObject(agentStates[receiver])
		status := rawString(state["status"])
		eventType := events.SubagentProgress
		switch status {
		case "pendingInit":
			eventType = events.SubagentStarted
		case "completed", "errored", "interrupted", "shutdown":
			eventType = events.SubagentCompleted
		default:
			if started && (tool == "spawnAgent" || tool == "resumeAgent") {
				eventType = events.SubagentStarted
			} else if !started && (tool == "closeAgent" || tool == "interruptAgent") {
				eventType = events.SubagentCompleted
			}
		}
		receiverPayload := make(map[string]any, len(payload)+3)
		for key, value := range payload {
			receiverPayload[key] = value
		}
		receiverPayload["toolName"] = tool
		receiverPayload["status"] = status
		receiverPayload["agentType"] = "codex"
		h.emit(eventType, "subagent", receiver, receiverPayload)
	}
}

func (h *processHandle) handleSubagentActivity(payload map[string]any, item map[string]json.RawMessage) {
	agentID := rawString(item["agentThreadId"])
	if agentID == "" {
		return
	}
	kind := rawString(item["kind"])
	payload["status"] = kind
	payload["agentType"] = "codex"
	payload["label"] = rawString(item["agentPath"])
	eventType := events.SubagentProgress
	switch kind {
	case "started":
		eventType = events.SubagentStarted
	case "completed", "interrupted":
		eventType = events.SubagentCompleted
	}
	h.emit(eventType, "subagent", agentID, payload)
}

func (h *processHandle) handlePlanUpdate(raw json.RawMessage, fields map[string]json.RawMessage) {
	var steps []struct {
		Step   string `json:"step"`
		Status string `json:"status"`
	}
	if json.Unmarshal(fields["plan"], &steps) != nil {
		return
	}
	for index, step := range steps {
		if strings.TrimSpace(step.Step) == "" {
			continue
		}
		status := step.Status
		if status == "inProgress" {
			status = "in_progress"
		}
		h.emit(events.TodoUpdated, "main_agent", "main", map[string]any{
			"runtime":      "codex",
			"runtimeEvent": raw,
			"todoId":       fmt.Sprintf("%d:%s", index, step.Step),
			"content":      step.Step,
			"status":       status,
		})
	}
}

func (h *processHandle) handleRuntimeError(fields map[string]json.RawMessage) {
	errorObject := rawObject(fields["error"])
	message := boundedText(rawString(errorObject["message"]), 2048)
	willRetry := rawBool(fields["willRetry"])
	if !willRetry {
		h.stateMu.Lock()
		h.turnFailed = true
		h.turnError = message
		h.stateMu.Unlock()
	}
	h.emit(events.Notice, "system", "", map[string]any{
		"kind":      "codex.error",
		"message":   message,
		"turnId":    rawString(fields["turnId"]),
		"willRetry": willRetry,
	})
}

func (h *processHandle) handleThreadStatus(raw json.RawMessage, fields map[string]json.RawMessage) {
	status := rawObject(fields["status"])
	typeName := rawString(status["type"])
	if typeName != "idle" && typeName != "systemError" {
		return
	}
	h.stateMu.Lock()
	turnID := h.activeTurnID
	runID := h.currentRunID
	failed := h.turnFailed || h.turnInterrupted || typeName == "systemError"
	interrupted := h.turnInterrupted
	errorMessage := h.turnError
	if turnID == "" {
		if typeName == "idle" && h.status != "stopping" && h.status != "exited" {
			h.status = "ready"
		}
		h.stateMu.Unlock()
		return
	}
	h.lastFinishedTurnID = turnID
	h.activeTurnID = ""
	h.currentRunID = ""
	h.turnFailed = false
	h.turnInterrupted = false
	h.turnError = ""
	if h.status != "stopping" && h.status != "exited" {
		h.status = "ready"
	}
	h.stateMu.Unlock()
	payload := map[string]any{
		"runtime":          "codex",
		"runtimeEvent":     raw,
		"threadId":         rawString(fields["threadId"]),
		"turnId":           turnID,
		"status":           typeName,
		"completionSource": "thread.status",
	}
	if failed {
		if interrupted {
			payload["reason"] = "interrupted"
		} else {
			payload["reason"] = firstNonEmpty(errorMessage, "thread_system_error")
		}
		h.emitForRun(runID, events.RunFailed, "main_agent", "main", payload)
		return
	}
	h.emitForRun(runID, events.RunCompleted, "main_agent", "main", payload)
}

func normalizedPayload(raw json.RawMessage, fields map[string]json.RawMessage) map[string]any {
	return map[string]any{
		"runtime":      "codex",
		"runtimeEvent": raw,
		"threadId":     rawString(fields["threadId"]),
		"turnId":       rawString(fields["turnId"]),
	}
}

func itemFailed(item map[string]json.RawMessage) bool {
	status := strings.ToLower(rawString(item["status"]))
	return strings.Contains(status, "fail") || strings.Contains(status, "error") || strings.Contains(status, "declin")
}

func rawObject(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return nil
	}
	return object
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func rawBool(raw json.RawMessage) bool {
	var value bool
	_ = json.Unmarshal(raw, &value)
	return value
}

func decodeRaw(raw json.RawMessage) any {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
