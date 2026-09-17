package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	hostv1 "agentbox/api"
	"agentbox/internal/domain"
	hostclient "agentbox/internal/host"
	runtimeapi "agentbox/internal/runtime"
	ompruntime "agentbox/internal/runtime/omp"
	"github.com/google/uuid"
)

const forcedStopGrace = 5 * time.Second

type EventPublisher interface {
	PublishRuntimeEvent(ctx context.Context, event runtimeapi.Event) (uint64, error)
}

// AdapterRegistration describes a successfully probed runtime available for new boxes.
type AdapterRegistration struct {
	Adapter            runtimeapi.Adapter
	Version            string
	Capabilities       runtimeapi.Capabilities
	StaticApprovalMode string
}

type managedRuntime struct {
	opMu sync.Mutex
	mu   sync.Mutex

	boxID             string
	runtimeInstanceID string
	runtimeType       string
	runtimeVersion    string
	workspace         string
	adapter           runtimeapi.Adapter
	handle            runtimeapi.Handle
	status            string
	processID         int64
	sessionRef        string
	capabilities      runtimeapi.Capabilities
	startedAt         time.Time
	lastEventAt       time.Time
	runID             string
	runStartedAt      time.Time
	runCancel         context.CancelFunc
	runGeneration     uint64
}

type Manager struct {
	adapters    map[runtimeapi.Type]AdapterRegistration
	guard       *WorkspaceGuard
	commandGate sync.RWMutex
	state       *StateStore

	mu             sync.Mutex
	boxes          map[string]*managedRuntime
	publisher      EventPublisher
	maxActiveBoxes int
	maxRunDuration time.Duration
	shuttingDown   bool

	ctx       context.Context
	cancel    context.CancelFunc
	eventWG   sync.WaitGroup
	fatalOnce sync.Once
	fatal     chan error
}

func NewManager(adapters []AdapterRegistration, guard *WorkspaceGuard, state *StateStore, maxActiveBoxes int, maxRunDuration time.Duration) (*Manager, error) {
	if guard == nil || state == nil {
		return nil, errors.New("workspace guard and state store are required")
	}
	if maxActiveBoxes <= 0 || maxRunDuration <= 0 {
		return nil, errors.New("runtime limits must be positive")
	}
	registry := make(map[runtimeapi.Type]AdapterRegistration, len(adapters))
	for _, registration := range adapters {
		if registration.Adapter == nil {
			return nil, errors.New("runtime adapter is required")
		}
		runtimeType := registration.Adapter.Name()
		if runtimeType == "" {
			return nil, errors.New("runtime adapter name is required")
		}
		if _, exists := registry[runtimeType]; exists {
			return nil, fmt.Errorf("runtime adapter %q is registered more than once", runtimeType)
		}
		registry[runtimeType] = registration
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		adapters:       registry,
		guard:          guard,
		state:          state,
		boxes:          make(map[string]*managedRuntime),
		maxActiveBoxes: maxActiveBoxes,
		maxRunDuration: maxRunDuration,
		ctx:            ctx,
		cancel:         cancel,
		fatal:          make(chan error, 1),
	}, nil
}

func (m *Manager) SetPublisher(publisher EventPublisher) error {
	if publisher == nil {
		return errors.New("event publisher is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.publisher != nil {
		return errors.New("event publisher is already configured")
	}
	m.publisher = publisher
	return nil
}

func (m *Manager) FatalErrors() <-chan error { return m.fatal }

func (m *Manager) ActiveBoxes() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.boxes)
}

func (m *Manager) HandleCommand(ctx context.Context, command *hostv1.HostCommand) (hostclient.CommandResult, error) {
	m.commandGate.RLock()
	defer m.commandGate.RUnlock()
	m.mu.Lock()
	shuttingDown := m.shuttingDown
	m.mu.Unlock()
	if shuttingDown {
		return failedResult("daemon_stopping", errors.New("daemon is shutting down")), nil
	}
	if command == nil {
		return failedResult("invalid_command", errors.New("command is required")), nil
	}
	if err := ctx.Err(); err != nil {
		return cancelledResult(err), nil
	}
	if command.BoxId == "" {
		return failedResult("invalid_command", errors.New("box ID is required")), nil
	}

	var result hostclient.CommandResult
	switch command.CommandType {
	case "runtime.start":
		result = m.handleStart(ctx, command)
	case "runtime.prompt":
		result = m.handleInput(ctx, command, runtimeapi.InputPrompt)
	case "runtime.steer":
		result = m.handleInput(ctx, command, runtimeapi.InputSteer)
	case "runtime.follow_up":
		result = m.handleInput(ctx, command, runtimeapi.InputFollowUp)
	case "runtime.approval_response":
		result = m.handleApprovalResponse(ctx, command)
	case "runtime.interrupt":
		result = m.handleInterrupt(ctx, command)
	case "runtime.stop":
		result = m.handleStop(ctx, command)
	case "runtime.inspect":
		result = m.handleInspect(ctx, command)
	default:
		result = failedResult("unsupported_command", fmt.Errorf("unsupported command type %q", command.CommandType))
	}
	return result, nil
}

type initialInputPayload struct {
	ID       string          `json:"id"`
	Message  string          `json:"message"`
	Delivery string          `json:"delivery"`
	Payload  json.RawMessage `json:"payload"`
}

type startPayload struct {
	Runtime           string               `json:"runtime"`
	Workspace         string               `json:"workspace"`
	SessionRef        string               `json:"sessionRef"`
	Model             string               `json:"model"`
	SystemPrompt      string               `json:"systemPrompt"`
	SystemPromptFile  string               `json:"systemPromptFile"`
	ApprovalMode      string               `json:"approvalMode"`
	Environment       map[string]string    `json:"environment"`
	SubagentEventMode string               `json:"subagentEventMode"`
	InitialInput      *initialInputPayload `json:"initialInput"`
}

func (m *Manager) handleStart(ctx context.Context, command *hostv1.HostCommand) hostclient.CommandResult {
	if command.RuntimeInstanceId == "" {
		return failedResult("invalid_command", errors.New("runtime instance ID is required"))
	}
	var payload startPayload
	if err := decodePayload(command.PayloadJson, &payload); err != nil {
		return failedResult("invalid_payload", err)
	}
	payload.Runtime = strings.TrimSpace(payload.Runtime)
	if payload.Runtime == "" {
		payload.Runtime = string(runtimeapi.TypeOMP)
	}
	registration, available := m.adapters[runtimeapi.Type(payload.Runtime)]
	if !available {
		return failedResult("unsupported_runtime", fmt.Errorf("runtime %q is not available", payload.Runtime))
	}
	if err := validateEnvironment(payload.Environment); err != nil {
		return failedResult("invalid_payload", err)
	}

	previous, hasPrevious := m.state.Get(command.BoxId)
	if payload.Workspace == "" && hasPrevious {
		payload.Workspace = previous.Workspace
	}
	if payload.SessionRef == "" && hasPrevious && previous.RuntimeType == payload.Runtime {
		payload.SessionRef = previous.SessionRef
	}
	workspace, err := m.guard.ResolveWorkspace(payload.Workspace)
	if err != nil {
		return failedResult("workspace_rejected", err)
	}
	if payload.SystemPromptFile != "" {
		payload.SystemPromptFile, err = m.guard.ResolveWorkspaceFile(workspace, payload.SystemPromptFile)
		if err != nil {
			return failedResult("workspace_rejected", err)
		}
	}
	if payload.SystemPrompt != "" {
		payload.SystemPromptFile, err = m.state.WritePromptFile(command.BoxId, payload.SystemPrompt)
		if err != nil {
			return failedResult("state_persistence", err)
		}
	}

	slot := &managedRuntime{
		boxID:             command.BoxId,
		runtimeInstanceID: command.RuntimeInstanceId,
		runtimeType:       payload.Runtime,
		runtimeVersion:    registration.Version,
		workspace:         workspace,
		adapter:           registration.Adapter,
		status:            "starting",
		sessionRef:        payload.SessionRef,
		capabilities:      registration.Capabilities,
		startedAt:         time.Now().UTC(),
		runID:             command.RunId,
	}
	slot.opMu.Lock()
	m.mu.Lock()
	if m.shuttingDown {
		m.mu.Unlock()
		slot.opMu.Unlock()
		return failedResult("daemon_stopping", errors.New("daemon is shutting down"))
	}
	if existing := m.boxes[command.BoxId]; existing != nil {
		m.mu.Unlock()
		slot.opMu.Unlock()
		existing.opMu.Lock()
		defer existing.opMu.Unlock()
		existing.mu.Lock()
		existingID := existing.runtimeInstanceID
		existingStatus := existing.status
		existing.mu.Unlock()
		if existingID == command.RuntimeInstanceId {
			if existingStatus == "failed" || existingStatus == "exited" {
				return failedResult("runtime_not_active", fmt.Errorf("runtime start finished with status %q", existingStatus))
			}
			return completedJSON(existing.resultMap())
		}
		return failedResult("box_runtime_conflict", errors.New("box already has an active runtime"))
	}
	if len(m.boxes) >= m.maxActiveBoxes {
		m.mu.Unlock()
		slot.opMu.Unlock()
		return failedResult("max_active_boxes", fmt.Errorf("active box limit %d reached", m.maxActiveBoxes))
	}
	m.boxes[command.BoxId] = slot
	m.mu.Unlock()

	if err := m.persistSlot(slot); err != nil {
		m.removeSlot(slot)
		slot.opMu.Unlock()
		return failedResult("state_persistence", err)
	}

	// A daemon-level static policy cannot be weakened by a remote start payload.
	approvalMode := payload.ApprovalMode
	if registration.StaticApprovalMode != "" {
		approvalMode = registration.StaticApprovalMode
	}

	handle, err := registration.Adapter.Start(ctx, runtimeapi.StartSpec{
		BoxID:             command.BoxId,
		RunID:             command.RunId,
		Workspace:         workspace,
		SessionRef:        payload.SessionRef,
		Model:             payload.Model,
		SystemPromptFile:  payload.SystemPromptFile,
		ApprovalMode:      approvalMode,
		Environment:       payload.Environment,
		SubagentEventMode: payload.SubagentEventMode,
	})
	if err != nil {
		slot.mu.Lock()
		slot.status = "failed"
		slot.mu.Unlock()
		persistErr := m.persistSlot(slot)
		m.removeSlot(slot)
		slot.opMu.Unlock()
		if persistErr != nil {
			err = errors.Join(err, persistErr)
		}
		return failedResult(runtimeErrorCode(err), err)
	}

	runtimeState, inspectErr := registration.Adapter.Inspect(ctx, handle)
	slot.mu.Lock()
	slot.handle = handle
	slot.sessionRef = handle.SessionRef()
	if inspectErr == nil {
		if runtimeState.Status != "" {
			slot.status = runtimeState.Status
		}
		if runtimeState.SessionRef != "" {
			slot.sessionRef = runtimeState.SessionRef
		}
		slot.capabilities = runtimeState.Capabilities
		if !runtimeState.StartedAt.IsZero() {
			slot.startedAt = runtimeState.StartedAt
		}
		slot.lastEventAt = runtimeState.LastEventAt
	}
	slot.mu.Unlock()
	if err := m.persistSlot(slot); err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), forcedStopGrace)
		_ = registration.Adapter.Stop(stopCtx, handle, runtimeapi.StopForce)
		cancel()
		m.removeSlot(slot)
		slot.opMu.Unlock()
		return failedResult("state_persistence", err)
	}
	slot.opMu.Unlock()

	m.eventWG.Add(1)
	go m.forwardEvents(slot, handle)
	if payload.InitialInput != nil && strings.TrimSpace(payload.InitialInput.Message) != "" {
		kind := runtimeapi.InputPrompt
		switch payload.InitialInput.Delivery {
		case string(domain.DeliverySteer):
			kind = runtimeapi.InputSteer
		case string(domain.DeliveryFollowUp):
			kind = runtimeapi.InputFollowUp
		}
		inputID := payload.InitialInput.ID
		if inputID == "" {
			inputID = command.IdempotencyKey + ":initial"
		}
		if kind == runtimeapi.InputPrompt || kind == runtimeapi.InputFollowUp {
			m.armRunDeadline(slot, command.RunId, time.Now().UTC())
		}
		if err := registration.Adapter.Send(ctx, handle, runtimeapi.Input{
			ID:      inputID,
			RunID:   command.RunId,
			Kind:    kind,
			Message: payload.InitialInput.Message,
			Payload: payload.InitialInput.Payload,
		}); err != nil {
			stopCtx, cancel := context.WithTimeout(context.Background(), forcedStopGrace)
			_ = registration.Adapter.Stop(stopCtx, handle, runtimeapi.StopForce)
			cancel()
			m.removeSlot(slot)
			return failedResult(runtimeErrorCode(err), err)
		}
	}
	return completedJSON(slot.resultMap())
}

type inputPayload struct {
	Message string          `json:"message"`
	Payload json.RawMessage `json:"payload"`
}

func (m *Manager) handleInput(ctx context.Context, command *hostv1.HostCommand, kind runtimeapi.InputKind) hostclient.CommandResult {
	var payload inputPayload
	if err := decodePayload(command.PayloadJson, &payload); err != nil {
		return failedResult("invalid_payload", err)
	}
	if strings.TrimSpace(payload.Message) == "" {
		return failedResult("invalid_payload", errors.New("message is required"))
	}
	if len(payload.Payload) > 0 {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(payload.Payload, &object); err != nil || object == nil {
			return failedResult("invalid_payload", errors.New("payload must be a JSON object"))
		}
	}
	slot, err := m.activeSlot(command)
	if err != nil {
		return failedResult("runtime_not_active", err)
	}
	slot.opMu.Lock()
	defer slot.opMu.Unlock()
	handle, err := slot.activeHandle()
	if err != nil {
		return failedResult("runtime_not_active", err)
	}
	deadlineArmed := kind == runtimeapi.InputPrompt || kind == runtimeapi.InputFollowUp
	if deadlineArmed {
		m.armRunDeadline(slot, command.RunId, time.Now().UTC())
	}
	inputID := command.IdempotencyKey
	if inputID == "" {
		inputID = command.CommandId
	}
	if err := slot.adapter.Send(ctx, handle, runtimeapi.Input{
		ID:      inputID,
		RunID:   command.RunId,
		Kind:    kind,
		Message: payload.Message,
		Payload: append(json.RawMessage(nil), payload.Payload...),
	}); err != nil {
		if deadlineArmed {
			slot.finishRunDeadline(command.RunId)
		}
		return commandFailure(ctx, err)
	}
	return completedJSON(slot.resultMap())
}

type approvalResponsePayload struct {
	ApprovalID string          `json:"approvalId"`
	Approved   bool            `json:"approved"`
	Payload    json.RawMessage `json:"payload"`
}

func (m *Manager) handleApprovalResponse(ctx context.Context, command *hostv1.HostCommand) hostclient.CommandResult {
	var payload approvalResponsePayload
	if err := decodePayload(command.PayloadJson, &payload); err != nil {
		return failedResult("invalid_payload", err)
	}
	payload.ApprovalID = strings.TrimSpace(payload.ApprovalID)
	if payload.ApprovalID == "" {
		return failedResult("invalid_payload", errors.New("approval ID is required"))
	}
	if len(payload.Payload) > 0 {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(payload.Payload, &object); err != nil || object == nil {
			return failedResult("invalid_payload", errors.New("payload must be a JSON object"))
		}
	}
	slot, err := m.activeSlot(command)
	if err != nil {
		return failedResult("runtime_not_active", err)
	}
	slot.opMu.Lock()
	defer slot.opMu.Unlock()
	handle, err := slot.activeHandle()
	if err != nil {
		return failedResult("runtime_not_active", err)
	}
	slot.mu.Lock()
	interactiveApproval := slot.capabilities.InteractiveApproval
	slot.mu.Unlock()
	if !interactiveApproval {
		return failedResult("interactive_approval_unsupported", fmt.Errorf("runtime %q does not support interactive approval", slot.runtimeType))
	}
	inputID := command.IdempotencyKey
	if inputID == "" {
		inputID = command.CommandId
	}
	if err := slot.adapter.Send(ctx, handle, runtimeapi.Input{
		ID:         inputID,
		RunID:      command.RunId,
		Kind:       runtimeapi.InputApprovalResponse,
		ApprovalID: payload.ApprovalID,
		Approved:   payload.Approved,
		Payload:    append(json.RawMessage(nil), payload.Payload...),
	}); err != nil {
		return commandFailure(ctx, err)
	}
	return completedJSON(slot.resultMap())
}

func (m *Manager) handleInterrupt(ctx context.Context, command *hostv1.HostCommand) hostclient.CommandResult {
	slot, err := m.activeSlot(command)
	if err != nil {
		if record, ok := m.persistedRuntime(command); ok {
			return completedJSON(persistedResultMap(record))
		}
		return failedResult("runtime_not_active", err)
	}
	slot.opMu.Lock()
	defer slot.opMu.Unlock()
	handle, err := slot.activeHandle()
	if err != nil {
		return failedResult("runtime_not_active", err)
	}
	if err := slot.adapter.Interrupt(ctx, handle); err != nil {
		return commandFailure(ctx, err)
	}
	return completedJSON(slot.resultMap())
}

type stopPayload struct {
	Mode runtimeapi.StopMode `json:"mode"`
}

func (m *Manager) handleStop(ctx context.Context, command *hostv1.HostCommand) hostclient.CommandResult {
	var payload stopPayload
	if err := decodePayload(command.PayloadJson, &payload); err != nil {
		return failedResult("invalid_payload", err)
	}
	if payload.Mode == "" {
		payload.Mode = runtimeapi.StopGraceful
	}
	if payload.Mode != runtimeapi.StopGraceful && payload.Mode != runtimeapi.StopForce {
		return failedResult("invalid_payload", fmt.Errorf("invalid stop mode %q", payload.Mode))
	}
	slot, err := m.activeSlot(command)
	if err != nil {
		if record, ok := m.persistedRuntime(command); ok {
			return completedJSON(persistedResultMap(record))
		}
		if command.RuntimeInstanceId != "" {
			return failedResult("runtime_not_found", err)
		}
		return completedJSON(map[string]any{
			"boxId":  command.BoxId,
			"status": "exited",
		})
	}
	if err := m.stopSlot(ctx, slot, payload.Mode); err != nil {
		return commandFailure(ctx, err)
	}
	return completedJSON(slot.resultMap())
}

func (m *Manager) handleInspect(ctx context.Context, command *hostv1.HostCommand) hostclient.CommandResult {
	slot, err := m.activeSlot(command)
	if err != nil {
		if record, ok := m.persistedRuntime(command); ok {
			return completedJSON(persistedResultMap(record))
		}
		return failedResult("runtime_not_found", err)
	}
	slot.opMu.Lock()
	defer slot.opMu.Unlock()
	handle, err := slot.activeHandle()
	if err != nil {
		return failedResult("runtime_not_active", err)
	}
	state, err := slot.adapter.Inspect(ctx, handle)
	if err != nil {
		return commandFailure(ctx, err)
	}
	slot.mu.Lock()
	slot.status = state.Status
	slot.sessionRef = state.SessionRef
	slot.capabilities = state.Capabilities
	slot.startedAt = state.StartedAt
	slot.lastEventAt = state.LastEventAt
	slot.mu.Unlock()
	if err := m.persistSlot(slot); err != nil {
		return failedResult("state_persistence", err)
	}
	return completedJSON(slot.resultMap())
}

func (m *Manager) activeSlot(command *hostv1.HostCommand) (*managedRuntime, error) {
	m.mu.Lock()
	slot := m.boxes[command.BoxId]
	m.mu.Unlock()
	if slot == nil {
		return nil, errors.New("box has no active runtime")
	}
	slot.mu.Lock()
	runtimeInstanceID := slot.runtimeInstanceID
	slot.mu.Unlock()
	if command.RuntimeInstanceId != "" && command.RuntimeInstanceId != runtimeInstanceID {
		return nil, fmt.Errorf("runtime instance %q is not active for box", command.RuntimeInstanceId)
	}
	return slot, nil
}

func (m *Manager) persistedRuntime(command *hostv1.HostCommand) (PersistedRuntime, bool) {
	record, ok := m.state.Get(command.BoxId)
	if !ok || (command.RuntimeInstanceId != "" && command.RuntimeInstanceId != record.RuntimeInstanceID) {
		return PersistedRuntime{}, false
	}
	return record, true
}

func (s *managedRuntime) activeHandle() (runtimeapi.Handle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle == nil || s.status == "exited" || s.status == "failed" {
		return nil, errors.New("runtime is not active")
	}
	return s.handle, nil
}

func (m *Manager) stopSlot(ctx context.Context, slot *managedRuntime, mode runtimeapi.StopMode) error {
	slot.opMu.Lock()
	defer slot.opMu.Unlock()
	slot.cancelRunDeadline()
	handle, err := slot.activeHandle()
	if err != nil {
		m.removeSlot(slot)
		return nil
	}
	stopCtx, cancel := context.WithTimeout(ctx, forcedStopGrace)
	err = slot.adapter.Stop(stopCtx, handle, mode)
	cancel()
	if err != nil && mode == runtimeapi.StopGraceful {
		forceCtx, forceCancel := context.WithTimeout(context.Background(), forcedStopGrace)
		forceErr := slot.adapter.Stop(forceCtx, handle, runtimeapi.StopForce)
		forceCancel()
		if forceErr == nil {
			err = nil
		} else {
			err = errors.Join(err, forceErr)
		}
	}
	if err != nil {
		return err
	}
	slot.mu.Lock()
	slot.status = "exited"
	slot.processID = 0
	slot.sessionRef = handle.SessionRef()
	slot.mu.Unlock()
	if err := m.persistSlot(slot); err != nil {
		return err
	}
	m.removeSlot(slot)
	return nil
}

func (m *Manager) removeSlot(slot *managedRuntime) {
	m.mu.Lock()
	if m.boxes[slot.boxID] == slot {
		delete(m.boxes, slot.boxID)
	}
	m.mu.Unlock()
}

func (m *Manager) persistSlot(slot *managedRuntime) error {
	slot.mu.Lock()
	record := PersistedRuntime{
		BoxID:             slot.boxID,
		RuntimeInstanceID: slot.runtimeInstanceID,
		RuntimeType:       slot.runtimeType,
		RuntimeVersion:    slot.runtimeVersion,
		Status:            slot.status,
		ProcessID:         slot.processID,
		SessionRef:        slot.sessionRef,
		Capabilities:      slot.capabilities,
		Workspace:         slot.workspace,
		RunID:             slot.runID,
		StartedAt:         slot.startedAt,
		LastEventAt:       slot.lastEventAt,
	}
	slot.mu.Unlock()
	return m.state.Put(record)
}

func (m *Manager) forwardEvents(slot *managedRuntime, handle runtimeapi.Handle) {
	defer m.eventWG.Done()
	for event := range slot.adapter.Events(handle) {
		event.BoxID = slot.boxID
		event.RuntimeInstanceID = slot.runtimeInstanceID
		if event.OccurredAt.IsZero() {
			event.OccurredAt = time.Now().UTC()
		}
		if event.ID == "" {
			event.ID = uuid.NewString()
		}
		publisher := m.eventPublisher()
		if publisher == nil {
			m.reportFatal(errors.New("runtime event publisher is not configured"))
			return
		}
		if _, err := publisher.PublishRuntimeEvent(m.ctx, event); err != nil {
			m.reportFatal(fmt.Errorf("persist runtime event: %w", err))
			return
		}
		persist := m.applyEvent(slot, handle, event)
		if persist {
			if err := m.persistSlot(slot); err != nil {
				m.reportFatal(fmt.Errorf("persist runtime snapshot: %w", err))
				return
			}
		}
	}

	slot.mu.Lock()
	if slot.handle == handle && slot.status != "failed" {
		slot.status = "exited"
		slot.processID = 0
		slot.sessionRef = handle.SessionRef()
	}
	slot.mu.Unlock()
	slot.cancelRunDeadline()
	if err := m.persistSlot(slot); err != nil {
		m.reportFatal(fmt.Errorf("persist exited runtime snapshot: %w", err))
	}
	m.removeSlot(slot)
}

func (m *Manager) eventPublisher() EventPublisher {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.publisher
}

func (m *Manager) applyEvent(slot *managedRuntime, handle runtimeapi.Handle, event runtimeapi.Event) bool {
	slot.mu.Lock()
	previousSession := slot.sessionRef
	previousStatus := slot.status
	previousPID := slot.processID
	slot.lastEventAt = event.OccurredAt
	if event.RunID != "" {
		slot.runID = event.RunID
	}
	if sessionRef := handle.SessionRef(); sessionRef != "" {
		slot.sessionRef = sessionRef
	}
	switch event.Type {
	case "runtime.starting":
		slot.status = "starting"
		var payload struct {
			PID int64 `json:"pid"`
		}
		if json.Unmarshal(event.Payload, &payload) == nil && payload.PID > 0 {
			slot.processID = payload.PID
		}
	case "runtime.ready", "run.completed", "run.failed":
		slot.status = "ready"
	case "run.started":
		slot.status = "busy"
	case "runtime.exited":
		slot.status = "exited"
		slot.processID = 0
	}
	changed := previousSession != slot.sessionRef || previousStatus != slot.status || previousPID != slot.processID
	terminalRun := event.Type == "run.completed" || event.Type == "run.failed" || event.Type == "runtime.exited"
	slot.mu.Unlock()
	if terminalRun {
		slot.finishRunDeadline(event.RunID)
	}
	return changed
}

func (m *Manager) armRunDeadline(slot *managedRuntime, runID string, startedAt time.Time) {
	m.mu.Lock()
	duration := m.maxRunDuration
	m.mu.Unlock()
	slot.armRunDeadline(m, runID, startedAt, duration)
}

func (s *managedRuntime) armRunDeadline(manager *Manager, runID string, startedAt time.Time, duration time.Duration) {
	s.mu.Lock()
	if s.runCancel != nil {
		s.runCancel()
	}
	ctx, cancel := context.WithCancel(manager.ctx)
	s.runCancel = cancel
	s.runID = runID
	s.runStartedAt = startedAt
	s.runGeneration++
	generation := s.runGeneration
	s.mu.Unlock()

	remaining := time.Until(startedAt.Add(duration))
	if remaining < 0 {
		remaining = 0
	}
	go manager.waitRunDeadline(ctx, s, runID, generation, remaining)
}

func (m *Manager) waitRunDeadline(ctx context.Context, slot *managedRuntime, runID string, generation uint64, remaining time.Duration) {
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	if !slot.runDeadlineCurrent(runID, generation) {
		return
	}

	handle, err := slot.activeHandle()
	if err == nil {
		interruptCtx, cancel := context.WithTimeout(context.Background(), forcedStopGrace)
		err = slot.adapter.Interrupt(interruptCtx, handle)
		cancel()
	}
	if err != nil {
		m.forceStopAfterDeadline(slot, runID, generation)
		return
	}

	grace := time.NewTimer(forcedStopGrace)
	defer grace.Stop()
	select {
	case <-ctx.Done():
		return
	case <-grace.C:
		if slot.runDeadlineCurrent(runID, generation) {
			m.forceStopAfterDeadline(slot, runID, generation)
		}
	}
}

func (m *Manager) forceStopAfterDeadline(slot *managedRuntime, runID string, generation uint64) {
	if !slot.runDeadlineCurrent(runID, generation) {
		return
	}
	handle, err := slot.activeHandle()
	if err == nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), forcedStopGrace)
		err = slot.adapter.Stop(stopCtx, handle, runtimeapi.StopForce)
		cancel()
	}
	if err != nil {
		m.reportFatal(fmt.Errorf("force stop runtime after maximum run duration: %w", err))
		return
	}
	slot.cancelRunDeadline()
	slot.mu.Lock()
	slot.status = "exited"
	slot.processID = 0
	slot.sessionRef = handle.SessionRef()
	slot.mu.Unlock()
	if err := m.persistSlot(slot); err != nil {
		m.reportFatal(fmt.Errorf("persist maximum-duration stop: %w", err))
	}
	m.removeSlot(slot)
}

func (s *managedRuntime) runDeadlineCurrent(runID string, generation uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runCancel != nil && s.runID == runID && s.runGeneration == generation
}

func (s *managedRuntime) finishRunDeadline(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runCancel != nil && (runID == "" || s.runID == runID) {
		s.runCancel()
		s.runCancel = nil
		s.runStartedAt = time.Time{}
	}
}

func (s *managedRuntime) cancelRunDeadline() {
	s.finishRunDeadline("")
}

func (m *Manager) OnConfigUpdate(_ context.Context, update *hostv1.ConfigUpdate) error {
	if update == nil {
		return errors.New("config update is required")
	}
	if update.MaxActiveBoxes < 0 || update.MaxActiveBoxes > 256 {
		return errors.New("max active boxes must be between 1 and 256 when provided")
	}
	if update.MaxRunDurationSeconds < 0 {
		return errors.New("maximum run duration cannot be negative")
	}
	m.mu.Lock()
	if update.MaxActiveBoxes > 0 {
		m.maxActiveBoxes = int(update.MaxActiveBoxes)
	}
	if update.MaxRunDurationSeconds > 0 {
		m.maxRunDuration = time.Duration(update.MaxRunDurationSeconds) * time.Second
	}
	duration := m.maxRunDuration
	slots := make([]*managedRuntime, 0, len(m.boxes))
	for _, slot := range m.boxes {
		slots = append(slots, slot)
	}
	m.mu.Unlock()

	if update.MaxRunDurationSeconds > 0 {
		for _, slot := range slots {
			slot.mu.Lock()
			runID := slot.runID
			startedAt := slot.runStartedAt
			active := slot.runCancel != nil
			slot.mu.Unlock()
			if active {
				slot.armRunDeadline(m, runID, startedAt, duration)
			}
		}
	}
	return nil
}

func (m *Manager) OnWelcome(context.Context, *hostv1.Welcome) error { return nil }

func (m *Manager) RuntimeSnapshot(context.Context) (*hostv1.RuntimeSnapshot, error) {
	records := m.state.Snapshot()
	sort.Slice(records, func(i, j int) bool { return records[i].BoxID < records[j].BoxID })
	processes := make([]*hostv1.RuntimeProcess, 0, len(records))
	for _, record := range records {
		capabilitiesJSON, err := json.Marshal(record.Capabilities)
		if err != nil {
			return nil, err
		}
		processes = append(processes, &hostv1.RuntimeProcess{
			RuntimeInstanceId: record.RuntimeInstanceID,
			BoxId:             record.BoxID,
			RuntimeType:       record.RuntimeType,
			RuntimeVersion:    record.RuntimeVersion,
			Status:            record.Status,
			ProcessId:         record.ProcessID,
			SessionRef:        record.SessionRef,
			CapabilitiesJson:  capabilitiesJSON,
		})
	}
	return &hostv1.RuntimeSnapshot{Processes: processes}, nil
}

func (m *Manager) Heartbeat(context.Context) (*hostv1.Heartbeat, error) {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return &hostv1.Heartbeat{
		UnixMillis:  time.Now().UnixMilli(),
		ActiveBoxes: int32(m.ActiveBoxes()),
		MemoryBytes: stats.Alloc,
	}, nil
}

func (m *Manager) Shutdown(ctx context.Context) error {
	m.commandGate.Lock()
	m.mu.Lock()
	m.shuttingDown = true
	slots := make([]*managedRuntime, 0, len(m.boxes))
	for _, slot := range m.boxes {
		slots = append(slots, slot)
	}
	m.mu.Unlock()
	m.commandGate.Unlock()

	var wait sync.WaitGroup
	errorsChannel := make(chan error, len(slots))
	for _, slot := range slots {
		wait.Add(1)
		go func(slot *managedRuntime) {
			defer wait.Done()
			if err := m.stopSlot(ctx, slot, runtimeapi.StopForce); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				errorsChannel <- err
			}
		}(slot)
	}
	stopsDone := make(chan struct{})
	go func() {
		wait.Wait()
		close(stopsDone)
	}()
	select {
	case <-ctx.Done():
		m.cancel()
		return ctx.Err()
	case <-stopsDone:
	}
	close(errorsChannel)

	eventsDone := make(chan struct{})
	go func() {
		m.eventWG.Wait()
		close(eventsDone)
	}()
	select {
	case <-ctx.Done():
		m.cancel()
		return ctx.Err()
	case <-eventsDone:
	}
	m.cancel()

	var joined error
	for err := range errorsChannel {
		joined = errors.Join(joined, err)
	}
	return joined
}

func (m *Manager) reportFatal(err error) {
	if err == nil {
		return
	}
	m.fatalOnce.Do(func() { m.fatal <- err })
}

func (s *managedRuntime) resultMap() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{
		"boxId":             s.boxID,
		"runtimeInstanceId": s.runtimeInstanceID,
		"runtime":           s.runtimeType,
		"runtimeVersion":    s.runtimeVersion,
		"status":            s.status,
		"processId":         s.processID,
		"sessionRef":        s.sessionRef,
		"capabilities":      s.capabilities,
		"workspace":         s.workspace,
		"runId":             s.runID,
	}
}

func persistedResultMap(record PersistedRuntime) map[string]any {
	return map[string]any{
		"boxId":             record.BoxID,
		"runtimeInstanceId": record.RuntimeInstanceID,
		"runtime":           record.RuntimeType,
		"runtimeVersion":    record.RuntimeVersion,
		"status":            record.Status,
		"processId":         record.ProcessID,
		"sessionRef":        record.SessionRef,
		"capabilities":      record.Capabilities,
		"workspace":         record.Workspace,
		"runId":             record.RunID,
	}
}

func decodePayload(data []byte, destination any) error {
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("payload contains multiple JSON values")
		}
		return err
	}
	return nil
}

func validateEnvironment(environment map[string]string) error {
	for key, value := range environment {
		if key == "" || strings.ContainsAny(key, "=\x00") {
			return fmt.Errorf("invalid environment key %q", key)
		}
		if strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("environment value for %q contains NUL", key)
		}
	}
	return nil
}

func completedJSON(value any) hostclient.CommandResult {
	encoded, err := json.Marshal(value)
	if err != nil {
		return failedResult("result_encoding", err)
	}
	return hostclient.CommandResult{
		Stage:      hostv1.CommandStage_COMMAND_STAGE_COMPLETED,
		ResultJSON: encoded,
	}
}

func failedResult(code string, err error) hostclient.CommandResult {
	message := ""
	if err != nil {
		message = err.Error()
	}
	return hostclient.CommandResult{
		Stage:        hostv1.CommandStage_COMMAND_STAGE_FAILED,
		ErrorCode:    code,
		ErrorMessage: message,
	}
}

func cancelledResult(err error) hostclient.CommandResult {
	return hostclient.CommandResult{
		Stage:        hostv1.CommandStage_COMMAND_STAGE_CANCELLED,
		ErrorCode:    "command_cancelled",
		ErrorMessage: err.Error(),
	}
}

func commandFailure(ctx context.Context, err error) hostclient.CommandResult {
	if ctx.Err() != nil {
		return cancelledResult(ctx.Err())
	}
	return failedResult(runtimeErrorCode(err), err)
}

func runtimeErrorCode(err error) string {
	var ompError *ompruntime.Error
	if errors.As(err, &ompError) && ompError.Code != "" {
		return ompError.Code
	}
	return "runtime_error"
}

var (
	_ hostclient.CommandHandler      = (*Manager)(nil)
	_ hostclient.ReconciliationHooks = (*Manager)(nil)
	_ hostclient.ServerHooks         = (*Manager)(nil)
	_ hostclient.HeartbeatProvider   = (*Manager)(nil)
)
