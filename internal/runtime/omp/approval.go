package omp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agentbox/internal/events"
	"agentbox/internal/runtime"
)

var interactiveUIMethods = map[string]struct{}{
	"confirm": {},
	"editor":  {},
	"input":   {},
	"select":  {},
}

var noticeUIMethods = map[string]struct{}{
	"notify":          {},
	"open_url":        {},
	"set_editor_text": {},
	"setEditorText":   {},
	"setStatus":       {},
	"setTitle":        {},
	"setWidget":       {},
}

type pendingApproval struct {
	id         string
	method     string
	runID      string
	title      string
	message    string
	toolName   string
	options    []string
	raw        json.RawMessage
	responding bool
}

type approvalResponsePayload struct {
	Decision  string  `json:"decision"`
	Value     *string `json:"value"`
	Confirmed *bool   `json:"confirmed"`
	Cancelled bool    `json:"cancelled"`
	Canceled  bool    `json:"canceled"`
}

type normalizedApprovalDecision struct {
	decision  string
	approved  bool
	cancelled bool
	value     *string
}

func (h *ompHandle) handleExtensionUIRequest(raw json.RawMessage, fields map[string]json.RawMessage) {
	method := strings.TrimSpace(rawString(fields["method"]))
	if _, ok := interactiveUIMethods[method]; ok {
		h.registerApprovalRequest(raw, fields, method)
		return
	}
	if method == "cancel" {
		targetID := firstNonEmpty(
			rawString(fields["targetId"]),
			rawString(fields["requestId"]),
			rawString(fields["id"]),
		)
		if targetID != "" {
			h.cancelApprovalFromRuntime(targetID, "runtime_cancelled")
		}
		h.emitNotice("OMP extension UI cancelled a pending request", raw)
		return
	}
	if _, ok := noticeUIMethods[method]; ok {
		h.emitNotice("OMP extension UI notice: "+method, raw)
		return
	}

	requestID := strings.TrimSpace(rawString(fields["id"]))
	if requestID == "" {
		h.fail(&Error{Op: "extension_ui", Code: codeProtocol, Err: fmt.Errorf("unsupported UI method %q omitted its request id", method)})
		return
	}
	response := map[string]any{
		"type":      "extension_ui_response",
		"id":        requestID,
		"cancelled": true,
	}
	if err := h.writeRequest(response); err != nil {
		h.fail(err)
		return
	}
	h.emitNotice("unsupported OMP extension UI method was cancelled: "+method, raw)
}

func (h *ompHandle) registerApprovalRequest(raw json.RawMessage, fields map[string]json.RawMessage, method string) {
	requestID := strings.TrimSpace(rawString(fields["id"]))
	if requestID == "" {
		h.fail(&Error{Op: "extension_ui", Code: codeProtocol, Err: fmt.Errorf("interactive UI method %q omitted its request id", method)})
		return
	}

	title, message := normalizeApprovalText(method, rawString(fields["title"]), rawString(fields["message"]))
	options := rawStringSlice(fields["options"])
	if options == nil {
		options = make([]string, 0)
	}
	if method == "confirm" && len(options) == 0 {
		options = []string{"Approve", "Deny"}
	}
	toolName := firstNonEmpty(
		rawString(fields["toolName"]),
		rawString(fields["tool"]),
		approvalToolName(title),
		approvalToolName(message),
		"runtime_ui",
	)
	runID := h.currentRunID()
	approval := &pendingApproval{
		id:       requestID,
		method:   method,
		runID:    runID,
		title:    title,
		message:  message,
		toolName: toolName,
		options:  append([]string(nil), options...),
		raw:      append(json.RawMessage(nil), raw...),
	}

	h.approvalMu.Lock()
	if _, exists := h.approvals[requestID]; exists {
		h.approvalMu.Unlock()
		h.fail(&Error{Op: "extension_ui", Code: codeProtocol, Err: fmt.Errorf("duplicate pending UI request id %q", requestID)})
		return
	}
	h.approvals[requestID] = approval
	h.approvalMu.Unlock()

	payload := map[string]any{
		"approvalId":       requestID,
		"runtimeRequestId": requestID,
		"requestId":        requestID,
		"method":           method,
		"title":            title,
		"message":          message,
		"options":          options,
		"toolName":         toolName,
		"tool":             toolName,
		"toolContext":      approvalToolContext(fields, title, message, toolName),
		"riskLevel":        "medium",
		"defaultDecision":  "deny",
		"supported":        true,
		"runtime":          "omp",
		"runtimeEvent":     raw,
	}
	if details := rawJSONArray(fields["optionDetails"]); details != nil {
		payload["optionDetails"] = details
	}
	h.emitForRun(runID, events.ApprovalRequested, "system", "", payload)
}

func (h *ompHandle) sendApprovalResponse(ctx context.Context, input runtime.Input) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	requestID := strings.TrimSpace(input.ApprovalID)
	if requestID == "" {
		return &Error{Op: "send", Code: codeInvalidSpec, Command: string(input.Kind), Err: errors.New("approval ID is empty")}
	}

	h.approvalMu.Lock()
	approval, ok := h.approvals[requestID]
	if ok && input.RunID != "" && approval.runID != input.RunID {
		h.approvalMu.Unlock()
		return &Error{Op: "send", Code: codeCommandFailed, Command: string(input.Kind), Err: fmt.Errorf("approval request %q belongs to run %q, not %q", requestID, approval.runID, input.RunID)}
	}
	if !ok && input.RunID != "" {
		for _, candidate := range h.approvals {
			if candidate.runID != input.RunID {
				continue
			}
			if approval != nil {
				h.approvalMu.Unlock()
				return &Error{Op: "send", Code: codeCommandFailed, Command: string(input.Kind), Err: fmt.Errorf("approval ID %q does not identify one of multiple pending requests for run %q", requestID, input.RunID)}
			}
			approval = candidate
		}
		ok = approval != nil
	}
	if !ok {
		h.approvalMu.Unlock()
		return &Error{Op: "send", Code: codeCommandFailed, Command: string(input.Kind), Err: fmt.Errorf("approval request %q is not pending", requestID)}
	}
	if approval.responding {
		h.approvalMu.Unlock()
		return &Error{Op: "send", Code: codeCommandFailed, Command: string(input.Kind), Err: fmt.Errorf("approval request %q is already being answered", approval.id)}
	}
	approval.responding = true
	h.approvalMu.Unlock()

	frame, decision, err := approvalResponseFrame(approval, input)
	if err != nil {
		h.restorePendingApproval(approval)
		return &Error{Op: "send", Code: codeInvalidSpec, Command: string(input.Kind), Err: err}
	}
	if err := h.writeRequest(frame); err != nil {
		h.restorePendingApproval(approval)
		return err
	}
	if !h.finishPendingApproval(approval) {
		return &Error{Op: "send", Code: codeCommandFailed, Command: string(input.Kind), Err: fmt.Errorf("approval request %q was cancelled while its response was being delivered", approval.id)}
	}

	h.emitApprovalResolved(approval, decision, frame)
	return nil
}

func approvalResponseFrame(approval *pendingApproval, input runtime.Input) (map[string]any, normalizedApprovalDecision, error) {
	decision, err := decodeApprovalDecision(input)
	if err != nil {
		return nil, normalizedApprovalDecision{}, err
	}
	frame := map[string]any{
		"type": "extension_ui_response",
		"id":   approval.id,
	}
	if decision.cancelled {
		frame["cancelled"] = true
		return frame, decision, nil
	}

	switch approval.method {
	case "confirm":
		frame["confirmed"] = decision.approved
	case "select":
		value := decision.value
		if value == nil {
			selected, ok := approvalDecisionOption(approval.options, decision.approved)
			if !ok && !decision.approved {
				decision.cancelled = true
				frame["cancelled"] = true
				return frame, decision, nil
			}
			if !ok {
				return nil, normalizedApprovalDecision{}, fmt.Errorf("approval request %q requires one of its explicit option values", approval.id)
			}
			value = &selected
		}
		selected, ok := canonicalApprovalOption(approval.options, *value)
		if !ok {
			return nil, normalizedApprovalDecision{}, fmt.Errorf("value %q is not an option for approval request %q", *value, approval.id)
		}
		decision.value = &selected
		frame["value"] = selected
	case "input", "editor":
		if decision.value == nil && !decision.approved {
			decision.cancelled = true
			frame["cancelled"] = true
			return frame, decision, nil
		}
		if decision.value == nil {
			return nil, normalizedApprovalDecision{}, fmt.Errorf("approval request %q requires a value or cancellation", approval.id)
		}
		frame["value"] = *decision.value
	default:
		return nil, normalizedApprovalDecision{}, fmt.Errorf("approval request %q has unsupported method %q", approval.id, approval.method)
	}
	return frame, decision, nil
}

func decodeApprovalDecision(input runtime.Input) (normalizedApprovalDecision, error) {
	result := normalizedApprovalDecision{approved: input.Approved}
	if len(input.Payload) == 0 {
		if input.Approved {
			result.decision = "approved"
		} else {
			result.decision = "denied"
		}
		return result, nil
	}
	var payload approvalResponsePayload
	if err := json.Unmarshal(input.Payload, &payload); err != nil {
		return normalizedApprovalDecision{}, fmt.Errorf("decode approval response payload: %w", err)
	}
	result.value = payload.Value
	if payload.Confirmed != nil {
		result.approved = *payload.Confirmed
	}
	decision := strings.ToLower(strings.TrimSpace(payload.Decision))
	switch decision {
	case "":
		if payload.Confirmed != nil {
			if result.approved {
				result.decision = "approved"
			} else {
				result.value = nil
				result.decision = "denied"
			}
		} else if result.value != nil {
			result.approved = true
			result.decision = "value"
		} else if result.approved {
			result.decision = "approved"
		} else {
			result.decision = "denied"
		}
	case "approve", "approved", "allow", "allowed", "yes":
		result.approved = true
		result.decision = "approved"
	case "deny", "denied", "reject", "rejected", "no":
		result.approved = false
		result.decision = "denied"
		result.value = nil
	case "cancel", "cancelled", "canceled", "expired":
		result.cancelled = true
		result.decision = "cancelled"
		result.value = nil
	case "value":
		if result.value == nil {
			return normalizedApprovalDecision{}, errors.New("value decision requires payload.value")
		}
		result.approved = true
		result.decision = "value"
	default:
		if result.value != nil {
			result.approved = true
			result.decision = "value"
		} else {
			value := payload.Decision
			result.value = &value
			result.approved = true
			result.decision = "value"
		}
	}
	if payload.Cancelled || payload.Canceled {
		result.cancelled = true
		result.decision = "cancelled"
		result.value = nil
	}
	return result, nil
}

func (h *ompHandle) restorePendingApproval(approval *pendingApproval) {
	h.approvalMu.Lock()
	if current, ok := h.approvals[approval.id]; ok && current == approval {
		approval.responding = false
	}
	h.approvalMu.Unlock()
}

func (h *ompHandle) finishPendingApproval(approval *pendingApproval) bool {
	h.approvalMu.Lock()
	defer h.approvalMu.Unlock()
	current, ok := h.approvals[approval.id]
	if !ok || current != approval {
		return false
	}
	delete(h.approvals, approval.id)
	return true
}

func (h *ompHandle) cancelApprovalFromRuntime(requestID, reason string) {
	h.approvalMu.Lock()
	approval, ok := h.approvals[requestID]
	if ok {
		delete(h.approvals, requestID)
	}
	h.approvalMu.Unlock()
	if !ok {
		return
	}
	h.emitApprovalResolved(approval, normalizedApprovalDecision{decision: reason, cancelled: true}, nil)
}

func (h *ompHandle) cancelPendingApprovals(reason string, deliver bool) error {
	h.approvalMu.Lock()
	approvals := make([]*pendingApproval, 0, len(h.approvals))
	for id, approval := range h.approvals {
		if approval.responding && deliver {
			continue
		}
		approval.responding = true
		delete(h.approvals, id)
		approvals = append(approvals, approval)
	}
	h.approvalMu.Unlock()

	var result error
	for _, approval := range approvals {
		frame := map[string]any{
			"type":      "extension_ui_response",
			"id":        approval.id,
			"cancelled": true,
		}
		var delivered map[string]any
		if deliver {
			if err := h.writeRequest(frame); err != nil {
				result = errors.Join(result, err)
			} else {
				delivered = frame
			}
		}
		h.emitApprovalResolved(approval, normalizedApprovalDecision{decision: reason, cancelled: true}, delivered)
	}
	return result
}

func (h *ompHandle) emitApprovalResolved(approval *pendingApproval, decision normalizedApprovalDecision, response map[string]any) {
	payload := map[string]any{
		"approvalId":       approval.id,
		"runtimeRequestId": approval.id,
		"requestId":        approval.id,
		"method":           approval.method,
		"title":            approval.title,
		"message":          approval.message,
		"toolName":         approval.toolName,
		"tool":             approval.toolName,
		"decision":         decision.decision,
		"approved":         decision.approved,
		"cancelled":        decision.cancelled,
		"runtime":          "omp",
		"runtimeEvent":     approval.raw,
	}
	if response != nil {
		payload["runtimeResponse"] = response
	}
	if decision.value != nil {
		payload["value"] = *decision.value
	}
	h.emitForRun(approval.runID, events.ApprovalResolved, "system", "", payload)
}

func normalizeApprovalText(method, title, message string) (string, string) {
	title = strings.TrimSpace(title)
	message = strings.TrimSpace(message)
	if message == "" && strings.Contains(title, "\n") {
		lines := strings.Split(title, "\n")
		title = strings.TrimSpace(lines[0])
		message = strings.TrimSpace(strings.Join(lines[1:], "\n"))
	}
	if title == "" {
		switch method {
		case "confirm":
			title = "Confirmation required"
		case "select":
			title = "Selection required"
		default:
			title = "Input required"
		}
	}
	return title, message
}

func approvalToolName(value string) string {
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if len(line) < len("Allow tool:") || !strings.EqualFold(line[:len("Allow tool:")], "Allow tool:") {
			continue
		}
		if name := strings.TrimSpace(line[len("Allow tool:"):]); name != "" {
			return name
		}
	}
	return ""
}

func approvalToolContext(fields map[string]json.RawMessage, title, message, toolName string) map[string]any {
	context := map[string]any{
		"toolName": toolName,
		"title":    title,
		"message":  message,
	}
	for _, key := range []string{"toolCallId", "input", "args", "arguments", "context"} {
		if value := rawJSONValue(fields[key]); value != nil {
			context[key] = value
		}
	}
	details := make(map[string]string)
	for _, text := range []string{title, message} {
		for _, line := range strings.Split(text, "\n") {
			key, value, ok := strings.Cut(line, ":")
			if !ok || strings.EqualFold(strings.TrimSpace(key), "Allow tool") {
				continue
			}
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			if key != "" && value != "" {
				details[key] = value
			}
		}
	}
	if len(details) > 0 {
		context["details"] = details
	}
	return context
}

func rawStringSlice(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var values []string
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	return values
}

func rawJSONArray(raw json.RawMessage) []any {
	if len(raw) == 0 {
		return nil
	}
	var values []any
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	return values
}

func rawJSONValue(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return value
}

func approvalDecisionOption(options []string, approved bool) (string, bool) {
	approvedOptions := []string{"approve", "approved", "allow", "allowed", "yes", "continue", "ok"}
	deniedOptions := []string{"deny", "denied", "reject", "rejected", "no"}
	candidates := deniedOptions
	if approved {
		candidates = approvedOptions
	}
	for _, option := range options {
		normalized := strings.ToLower(strings.TrimSpace(option))
		for _, candidate := range candidates {
			if normalized == candidate {
				return option, true
			}
		}
	}
	return "", false
}

func canonicalApprovalOption(options []string, value string) (string, bool) {
	for _, option := range options {
		if option == value {
			return option, true
		}
	}
	for _, option := range options {
		if strings.EqualFold(strings.TrimSpace(option), strings.TrimSpace(value)) {
			return option, true
		}
	}
	return "", false
}
