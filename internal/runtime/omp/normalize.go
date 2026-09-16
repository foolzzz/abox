package omp

import (
	"encoding/json"
	"strings"

	"agentbox/internal/events"
)

func (h *ompHandle) normalizeFrame(raw json.RawMessage, actorKindOverride, actorIDOverride string) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		h.fail(&Error{Op: "normalize", Code: codeProtocol, Err: err})
		return
	}
	frameType := rawString(fields["type"])
	payload := map[string]any{
		"runtime":      "omp",
		"runtimeEvent": raw,
	}

	actorKind := actorKindOverride
	actorID := actorIDOverride
	if actorKind == "" {
		actorKind = "main_agent"
		actorID = "main"
	}

	switch frameType {
	case "agent_start":
		h.setStatus("busy")
		h.emit(events.RunStarted, actorKind, actorID, payload)
	case "agent_end":
		if terminal := rawBoolPointer(fields["isTerminal"]); terminal != nil && !*terminal {
			h.emit(events.Notice, "system", "", map[string]any{
				"message":      "OMP emitted a non-terminal agent_end; more work is scheduled",
				"runtime":      "omp",
				"runtimeEvent": raw,
			})
			return
		}
		runID := h.currentRunID()
		h.setStatus("ready")
		if frameIndicatesFailure(fields) {
			h.emitForRun(runID, events.RunFailed, actorKind, actorID, payload)
		} else {
			h.emitForRun(runID, events.RunCompleted, actorKind, actorID, payload)
		}
		h.completeRun(runID)
	case "turn_start", "turn_end":
		// Agent lifecycle events provide the stable normalized run boundary.
	case "message_start":
		messageActorKind, messageActorID := messageActor(fields, actorKind, actorID)
		h.emit(events.MessageStarted, messageActorKind, messageActorID, payload)
	case "message_update":
		messageActorKind, messageActorID := messageActor(fields, actorKind, actorID)
		if delta := rawObject(fields["assistantMessageEvent"]); delta != nil {
			payload["delta"] = delta
		}
		h.emit(events.MessageDelta, messageActorKind, messageActorID, payload)
	case "message_end":
		messageActorKind, messageActorID := messageActor(fields, actorKind, actorID)
		h.emit(events.MessageCompleted, messageActorKind, messageActorID, payload)
	case "tool_execution_start":
		h.emit(events.ToolStarted, actorKind, actorID, payload)
	case "tool_execution_update":
		h.emit(events.ToolProgress, actorKind, actorID, payload)
	case "tool_execution_end":
		if frameIndicatesFailure(fields) {
			h.emit(events.ToolFailed, actorKind, actorID, payload)
		} else {
			h.emit(events.ToolCompleted, actorKind, actorID, payload)
		}
		if rawString(fields["toolName"]) == "todo" {
			h.emit(events.TodoUpdated, actorKind, actorID, payload)
		}
	case "tool_approval_requested":
		h.emit(events.ApprovalRequested, actorKind, actorID, payload)
	case "tool_approval_resolved":
		h.emit(events.ApprovalResolved, actorKind, actorID, payload)
	case "todo_reminder", "todo_auto_clear", "todo_update", "todo_updated":
		h.emit(events.TodoUpdated, actorKind, actorID, payload)
	case "subagent_lifecycle":
		subagentID := subagentID(fields)
		eventType := subagentLifecycleType(fields)
		h.emit(eventType, "subagent", subagentID, payload)
	case "subagent_progress":
		h.emit(events.SubagentProgress, "subagent", subagentID(fields), payload)
	case "subagent_event":
		subagentID := subagentID(fields)
		if nested := rawObject(fields["event"]); nested != nil && rawString(nested["type"]) != "" {
			nestedRaw, err := json.Marshal(nested)
			if err != nil {
				h.fail(&Error{Op: "normalize", Code: codeProtocol, Err: err})
				return
			}
			h.normalizeFrame(nestedRaw, "subagent", subagentID)
			return
		}
		h.emit(events.SubagentProgress, "subagent", subagentID, payload)
	case "prompt_result":
		if invoked := rawBoolPointer(fields["agentInvoked"]); invoked != nil && !*invoked {
			runID := h.promptRunID(rawString(fields["id"]))
			h.setStatus("ready")
			h.emitForRun(runID, events.RunCompleted, actorKind, actorID, map[string]any{
				"reason":       "local_only_prompt",
				"runtime":      "omp",
				"runtimeEvent": raw,
			})
			h.completeRun(runID)
			return
		}
		h.emitNotice("OMP prompt lifecycle update", raw)
	case "session_info_update":
		h.updateSessionReference(fields)
		h.emitNotice("OMP session information changed", raw)
	case "notice":
		message := firstNonEmpty(rawString(fields["message"]), rawString(fields["text"]), "OMP notice")
		h.emit(events.Notice, "system", "", map[string]any{
			"message":      message,
			"runtime":      "omp",
			"runtimeEvent": raw,
		})
	case "extension_error":
		h.emit(events.Notice, "system", "", map[string]any{
			"message":      firstNonEmpty(rawString(fields["error"]), "OMP extension error"),
			"runtime":      "omp",
			"runtimeEvent": raw,
		})
	case "available_commands_update", "command_output", "config_update", "extension_ui_request",
		"host_tool_call", "host_tool_cancel", "host_uri_request", "host_uri_cancel",
		"auto_compaction_start", "auto_compaction_end", "auto_retry_start", "auto_retry_end",
		"retry_fallback_applied", "retry_fallback_succeeded", "model_changed",
		"thinking_level_changed", "ttsr_triggered", "irc_message", "goal_updated":
		h.emitNotice("OMP runtime frame: "+frameType, raw)
	default:
		h.emitNotice("unrecognized OMP RPC frame", raw)
	}
}

func (h *ompHandle) emitNotice(message string, raw json.RawMessage) {
	var header frameHeader
	_ = json.Unmarshal(raw, &header)
	h.emit(events.Notice, "daemon", "", map[string]any{
		"message":      message,
		"frameType":    header.Type,
		"runtime":      "omp",
		"runtimeEvent": raw,
	})
}

func (h *ompHandle) updateSessionReference(fields map[string]json.RawMessage) {
	sessionRef := firstNonEmpty(rawString(fields["sessionFile"]), rawString(fields["sessionId"]))
	if sessionRef == "" {
		if data := rawObject(fields["data"]); data != nil {
			sessionRef = firstNonEmpty(rawString(data["sessionFile"]), rawString(data["sessionId"]))
		}
	}
	if sessionRef == "" {
		return
	}
	h.stateMu.Lock()
	h.sessionRef = sessionRef
	h.state.SessionRef = sessionRef
	h.stateMu.Unlock()
}

func messageActor(fields map[string]json.RawMessage, fallbackKind, fallbackID string) (string, string) {
	message := rawObject(fields["message"])
	if rawString(message["role"]) == "user" {
		return "user", ""
	}
	return fallbackKind, fallbackID
}

func subagentID(fields map[string]json.RawMessage) string {
	if id := firstNonEmpty(rawString(fields["subagentId"]), rawString(fields["agentId"])); id != "" {
		return id
	}
	for _, key := range []string{"subagent", "agent", "record", "progress"} {
		nested := rawObject(fields[key])
		if id := firstNonEmpty(rawString(nested["id"]), rawString(nested["subagentId"]), rawString(nested["agentId"])); id != "" {
			return id
		}
	}
	return ""
}

func subagentLifecycleType(fields map[string]json.RawMessage) string {
	state := strings.ToLower(strings.Join([]string{
		rawString(fields["event"]),
		rawString(fields["action"]),
		rawString(fields["status"]),
		rawString(fields["lifecycle"]),
	}, " "))

	switch {
	case strings.Contains(state, "start"), strings.Contains(state, "spawn"), strings.Contains(state, "create"), strings.Contains(state, "running"):
		return events.SubagentStarted
	case strings.Contains(state, "complete"), strings.Contains(state, "finish"), strings.Contains(state, "settle"), strings.Contains(state, "exit"), strings.Contains(state, "fail"), strings.Contains(state, "cancel"), strings.Contains(state, "tombstone"):
		return events.SubagentCompleted
	default:
		return events.SubagentProgress
	}
}

func frameIndicatesFailure(fields map[string]json.RawMessage) bool {
	if value := rawBoolPointer(fields["isError"]); value != nil && *value {
		return true
	}
	if rawString(fields["error"]) != "" || rawString(fields["stopReason"]) == "error" {
		return true
	}
	for _, key := range []string{"result", "message"} {
		if nested := rawObject(fields[key]); nested != nil && frameIndicatesFailure(nested) {
			return true
		}
	}
	var messages []map[string]json.RawMessage
	if len(fields["messages"]) > 0 && json.Unmarshal(fields["messages"], &messages) == nil && len(messages) > 0 {
		return frameIndicatesFailure(messages[len(messages)-1])
	}
	return false
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

func rawBoolPointer(raw json.RawMessage) *bool {
	if len(raw) == 0 {
		return nil
	}
	var value bool
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func rawObject(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var value map[string]json.RawMessage
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
