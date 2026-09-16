package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"agentbox/internal/events"
	runtimeapi "agentbox/internal/runtime"
	"github.com/google/uuid"
)

const (
	defaultBinary          = "claude"
	defaultMaxMessageBytes = 4 << 20
	defaultStderrBytes     = 64 << 10
	defaultEventBuffer     = 256
	defaultStopGrace       = 5 * time.Second
)

type Option func(*Adapter)

type Adapter struct {
	binary          string
	maxMessageBytes int
	stderrBytes     int
	eventBuffer     int
	stopGrace       time.Duration
}

var _ runtimeapi.Adapter = (*Adapter)(nil)

func New(options ...Option) *Adapter {
	a := &Adapter{
		binary:          defaultBinary,
		maxMessageBytes: defaultMaxMessageBytes,
		stderrBytes:     defaultStderrBytes,
		eventBuffer:     defaultEventBuffer,
		stopGrace:       defaultStopGrace,
	}
	for _, option := range options {
		if option != nil {
			option(a)
		}
	}
	return a
}

func WithBinary(binary string) Option {
	return func(a *Adapter) {
		if strings.TrimSpace(binary) != "" {
			a.binary = binary
		}
	}
}

func WithMaxMessageBytes(limit int) Option {
	return func(a *Adapter) {
		if limit > 0 {
			a.maxMessageBytes = limit
		}
	}
}

func WithStderrBytes(limit int) Option {
	return func(a *Adapter) {
		if limit > 0 {
			a.stderrBytes = limit
		}
	}
}

func WithEventBuffer(size int) Option {
	return func(a *Adapter) {
		if size > 0 {
			a.eventBuffer = size
		}
	}
}

func WithStopGracePeriod(period time.Duration) Option {
	return func(a *Adapter) {
		if period > 0 {
			a.stopGrace = period
		}
	}
}

func (a *Adapter) Name() runtimeapi.Type {
	return runtimeapi.TypeClaude
}

func (a *Adapter) Probe(ctx context.Context) (string, runtimeapi.Capabilities, error) {
	cmd := exec.CommandContext(ctx, a.binary, "--version")
	output, err := cmd.CombinedOutput()
	version := strings.TrimSpace(string(output))
	if err != nil {
		return version, capabilities(), &OperationError{
			Operation: "probe",
			Cause:     err,
			Detail:    version,
		}
	}
	return version, capabilities(), nil
}

func capabilities() runtimeapi.Capabilities {
	return runtimeapi.Capabilities{
		Steer:               false,
		FollowUp:            true,
		SubagentEvents:      true,
		TodoEvents:          true,
		Resume:              true,
		HostTools:           false,
		InteractiveApproval: false,
	}
}

type processHandle struct {
	adapter *Adapter
	id      string
	spec    runtimeapi.StartSpec
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  *tailBuffer

	events     chan runtimeapi.Event
	done       chan struct{}
	readerDone chan struct{}
	writeGate  chan struct{}

	mu               sync.Mutex
	sessionRef       string
	status           string
	startedAt        time.Time
	lastEventAt      time.Time
	sequence         uint64
	turnSequence     uint64
	current          *turn
	pending          *turn
	guardNextReplay  bool
	guardRunID       string
	policy           PermissionPolicy
	protocolErr      error
	exitErr          error
	stopRequested    bool
	interruptRequest string
}

type turn struct {
	sequence       uint64
	inputID        string
	runID          string
	residualRunID  string
	kind           runtimeapi.InputKind
	message        string
	wire           []byte
	interrupted    bool
	awaitingReplay bool
	parser         *turnParser
}

func (h *processHandle) ID() string {
	return h.id
}

func (h *processHandle) SessionRef() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessionRef
}

func (a *Adapter) Start(ctx context.Context, spec runtimeapi.StartSpec) (runtimeapi.Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	policy, err := StaticPermissionPolicy(spec.ApprovalMode)
	if err != nil {
		return nil, err
	}

	sessionRef := strings.TrimSpace(spec.SessionRef)
	newSession := sessionRef == ""
	if newSession {
		sessionRef = uuid.NewString()
	}

	args, err := a.commandArgs(spec, policy, sessionRef, newSession)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(a.binary, args...)
	if spec.Workspace != "" {
		cmd.Dir = spec.Workspace
	}
	cmd.Env = mergedEnvironment(spec.Environment)
	configureProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, &OperationError{Operation: "open stdin", Cause: err}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, &OperationError{Operation: "open stdout", Cause: err}
	}
	stderr := newTailBuffer(a.stderrBytes)
	cmd.Stderr = stderr

	now := time.Now().UTC()
	h := &processHandle{
		adapter:    a,
		id:         uuid.NewString(),
		spec:       spec,
		cmd:        cmd,
		stdin:      stdin,
		stdout:     stdout,
		stderr:     stderr,
		events:     make(chan runtimeapi.Event, a.eventBuffer),
		done:       make(chan struct{}),
		readerDone: make(chan struct{}),
		writeGate:  make(chan struct{}, 1),
		sessionRef: sessionRef,
		status:     "starting",
		startedAt:  now,
		policy:     policy,
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, &OperationError{Operation: "start", Cause: err}
	}
	if err := ctx.Err(); err != nil {
		_ = signalProcessGroup(cmd.Process, os.Kill)
		_ = stdin.Close()
		_ = stdout.Close()
		_ = cmd.Wait()
		return nil, err
	}

	h.emit(events.RuntimeStarting, "daemon", "", map[string]any{
		"binary":         a.binary,
		"pid":            cmd.Process.Pid,
		"sessionId":      sessionRef,
		"resumed":        !newSession,
		"permissionMode": policy.Mode,
	})

	go h.readOutput()
	go h.waitProcess()
	return h, nil
}

func (a *Adapter) commandArgs(spec runtimeapi.StartSpec, policy PermissionPolicy, sessionRef string, newSession bool) ([]string, error) {
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--replay-user-messages",
		"--forward-subagent-text",
	}
	policyArgs, err := policy.commandArgs()
	if err != nil {
		return nil, err
	}
	args = append(args, policyArgs...)

	if newSession {
		args = append(args, "--session-id", sessionRef)
	} else {
		args = append(args, "--resume", sessionRef)
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.SystemPromptFile != "" {
		args = append(args, "--append-system-prompt-file", spec.SystemPromptFile)
	}
	return args, nil
}

func mergedEnvironment(overrides map[string]string) []string {
	values := make(map[string]string, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[name] = value
		}
	}
	for name, value := range overrides {
		values[name] = value
	}

	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)

	environment := make([]string, 0, len(names))
	for _, name := range names {
		environment = append(environment, name+"="+values[name])
	}
	return environment
}

func (a *Adapter) Send(ctx context.Context, handle runtimeapi.Handle, input runtimeapi.Input) error {
	h, err := a.processHandle(handle)
	if err != nil {
		return err
	}

	switch input.Kind {
	case runtimeapi.InputPrompt, runtimeapi.InputFollowUp:
	case runtimeapi.InputApprovalResponse:
		return &OperationError{
			Operation: "send approval response",
			Cause:     ErrInteractiveApprovalUnsupported,
			Detail:    "Claude is running with --permission-prompts none",
		}
	default:
		return &OperationError{
			Operation: "send",
			Cause:     ErrUnsupportedInput,
			Detail:    fmt.Sprintf("input kind %q", input.Kind),
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	wire, err := json.Marshal(userInputMessage{
		Type: "user",
		Message: userMessage{
			Role:    "user",
			Content: input.Message,
		},
	})
	if err != nil {
		return &OperationError{Operation: "encode prompt", Cause: err}
	}
	if len(wire) > a.maxMessageBytes {
		return &OperationError{
			Operation: "encode prompt",
			Cause:     &ProtocolError{Kind: "message_too_large", Detail: fmt.Sprintf("%d bytes exceeds %d-byte limit", len(wire), a.maxMessageBytes)},
		}
	}

	h.mu.Lock()
	if h.status == "exited" {
		h.mu.Unlock()
		return &OperationError{Operation: "send", Cause: ErrProcessExited}
	}
	h.turnSequence++
	runID := input.RunID
	if runID == "" {
		runID = h.spec.RunID
	}
	t := &turn{
		sequence: h.turnSequence,
		inputID:  input.ID,
		runID:    runID,
		kind:     input.Kind,
		message:  input.Message,
		wire:     wire,
		parser:   newTurnParser(),
	}

	queued := false
	if h.current == nil {
		if h.guardNextReplay {
			t.awaitingReplay = true
			t.residualRunID = h.guardRunID
			h.guardNextReplay = false
			h.guardRunID = ""
		}
		h.current = t
		h.status = "busy"
	} else if h.current.interrupted {
		if h.pending != nil {
			h.mu.Unlock()
			return &OperationError{Operation: "send", Cause: ErrTurnQueued}
		}
		t.awaitingReplay = true
		t.residualRunID = h.current.runID
		h.pending = t
		queued = true
	} else {
		h.mu.Unlock()
		return &OperationError{Operation: "send", Cause: ErrTurnActive}
	}
	h.mu.Unlock()

	if queued {
		h.emitForRun(t.runID, events.Notice, "daemon", "", map[string]any{
			"kind":       "turn.queued",
			"inputId":    t.inputID,
			"turnNumber": t.sequence,
			"reason":     "waiting for the interrupted turn result boundary",
		})
		return nil
	}

	return h.startTurn(ctx, t, false)
}

func (h *processHandle) startTurn(ctx context.Context, t *turn, afterInterrupt bool) error {
	if err := h.acquireWriter(ctx); err != nil {
		h.failTurn(t, "write_failed", err)
		return &OperationError{Operation: "write prompt", Cause: err}
	}
	defer h.releaseWriter()

	h.emitForRun(t.runID, events.RunStarted, "daemon", "", map[string]any{
		"inputId":              t.inputID,
		"inputKind":            t.kind,
		"turnNumber":           t.sequence,
		"queuedAfterInterrupt": afterInterrupt,
	})
	if err := h.writeLineHeld(t.wire); err != nil {
		h.failTurn(t, "write_failed", err)
		return &OperationError{Operation: "write prompt", Cause: err}
	}
	return nil
}

func (a *Adapter) Interrupt(ctx context.Context, handle runtimeapi.Handle) error {
	h, err := a.processHandle(handle)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	requestID := uuid.NewString()
	h.mu.Lock()
	if h.status == "exited" {
		h.mu.Unlock()
		return nil
	}
	if h.current == nil {
		h.mu.Unlock()
		return nil
	}
	if h.current.interrupted {
		h.mu.Unlock()
		return nil
	}
	interruptedTurn := h.current
	interruptedTurn.interrupted = true
	h.interruptRequest = requestID
	h.mu.Unlock()

	request := controlRequest{
		Type:      "control_request",
		RequestID: requestID,
		Request: controlRequestBody{
			Subtype: "interrupt",
		},
	}
	line, err := json.Marshal(request)
	if err != nil {
		h.revertInterrupt(interruptedTurn, requestID)
		return &OperationError{Operation: "encode interrupt", Cause: err}
	}
	if err := h.writeLine(ctx, line); err != nil {
		h.revertInterrupt(interruptedTurn, requestID)
		return &OperationError{Operation: "interrupt", Cause: err}
	}
	h.emitForRun(interruptedTurn.runID, events.Notice, "daemon", "", map[string]any{
		"kind":      "turn.interrupt_requested",
		"requestId": requestID,
	})
	return nil
}

func (h *processHandle) revertInterrupt(t *turn, requestID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current == t && h.interruptRequest == requestID {
		t.interrupted = false
		h.interruptRequest = ""
	}
}

func (a *Adapter) SetPermissionPolicy(ctx context.Context, handle runtimeapi.Handle, policy PermissionPolicy) error {
	h, err := a.processHandle(handle)
	if err != nil {
		return err
	}
	resolved, err := StaticPermissionPolicy(policy.Mode)
	if err != nil {
		return err
	}

	requestID := uuid.NewString()
	line, err := json.Marshal(controlRequest{
		Type:      "control_request",
		RequestID: requestID,
		Request: controlRequestBody{
			Subtype: "set_permission_mode",
			Mode:    resolved.Mode,
		},
	})
	if err != nil {
		return &OperationError{Operation: "encode permission policy", Cause: err}
	}
	if err := h.writeLine(ctx, line); err != nil {
		return &OperationError{Operation: "set permission policy", Cause: err}
	}

	h.mu.Lock()
	h.policy = resolved
	h.mu.Unlock()
	h.emit(events.Notice, "daemon", "", map[string]any{
		"kind":           "permission_policy.updated",
		"permissionMode": resolved.Mode,
		"requestId":      requestID,
	})
	return nil
}

func (a *Adapter) Stop(ctx context.Context, handle runtimeapi.Handle, mode runtimeapi.StopMode) error {
	h, err := a.processHandle(handle)
	if err != nil {
		return err
	}

	select {
	case <-h.done:
		return nil
	default:
	}

	h.mu.Lock()
	h.stopRequested = true
	if h.status != "exited" {
		h.status = "stopping"
	}
	h.mu.Unlock()
	_ = h.stdin.Close()

	signal := gracefulStopSignal()
	if mode == runtimeapi.StopForce {
		signal = osKillSignal()
	}
	if err := signalProcessGroup(h.cmd.Process, signal); err != nil {
		return &OperationError{Operation: "stop process group", Cause: err}
	}

	if mode == runtimeapi.StopForce {
		select {
		case <-h.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	timer := time.NewTimer(a.stopGrace)
	defer timer.Stop()
	select {
	case <-h.done:
		return nil
	case <-ctx.Done():
		_ = signalProcessGroup(h.cmd.Process, osKillSignal())
		return ctx.Err()
	case <-timer.C:
		if err := signalProcessGroup(h.cmd.Process, osKillSignal()); err != nil {
			return &OperationError{Operation: "force stop process group", Cause: err}
		}
		select {
		case <-h.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (a *Adapter) Events(handle runtimeapi.Handle) <-chan runtimeapi.Event {
	h, err := a.processHandle(handle)
	if err != nil {
		closed := make(chan runtimeapi.Event)
		close(closed)
		return closed
	}
	return h.events
}

func (a *Adapter) Inspect(ctx context.Context, handle runtimeapi.Handle) (runtimeapi.State, error) {
	if err := ctx.Err(); err != nil {
		return runtimeapi.State{}, err
	}
	h, err := a.processHandle(handle)
	if err != nil {
		return runtimeapi.State{}, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	return runtimeapi.State{
		Status:       h.status,
		SessionRef:   h.sessionRef,
		Capabilities: capabilities(),
		StartedAt:    h.startedAt,
		LastEventAt:  h.lastEventAt,
	}, nil
}

func (a *Adapter) processHandle(handle runtimeapi.Handle) (*processHandle, error) {
	h, ok := handle.(*processHandle)
	if !ok || h == nil || h.adapter != a {
		return nil, ErrInvalidHandle
	}
	return h, nil
}

func (h *processHandle) writeLine(ctx context.Context, line []byte) error {
	if err := h.acquireWriter(ctx); err != nil {
		return err
	}
	defer h.releaseWriter()
	return h.writeLineHeld(line)
}

func (h *processHandle) acquireWriter(ctx context.Context) error {
	select {
	case h.writeGate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-h.done:
		return ErrProcessExited
	}
	if err := ctx.Err(); err != nil {
		h.releaseWriter()
		return err
	}
	select {
	case <-h.done:
		h.releaseWriter()
		return ErrProcessExited
	default:
		return nil
	}
}

func (h *processHandle) releaseWriter() {
	<-h.writeGate
}

func (h *processHandle) writeLineHeld(line []byte) error {
	if len(line) > h.adapter.maxMessageBytes {
		return &ProtocolError{Kind: "message_too_large", Detail: fmt.Sprintf("%d bytes exceeds %d-byte limit", len(line), h.adapter.maxMessageBytes)}
	}
	payload := make([]byte, len(line)+1)
	copy(payload, line)
	payload[len(line)] = '\n'
	written := 0
	for written < len(payload) {
		n, err := h.stdin.Write(payload[written:])
		written += n
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func (h *processHandle) failTurn(t *turn, reason string, cause error) {
	h.mu.Lock()
	if h.current != t {
		h.mu.Unlock()
		return
	}
	h.current = nil
	if h.pending == nil && h.status != "stopping" && h.status != "exited" {
		h.status = "ready"
	}
	if t.awaitingReplay {
		h.guardNextReplay = true
		h.guardRunID = t.residualRunID
	}
	h.mu.Unlock()

	h.emitForRun(t.runID, events.RunFailed, "daemon", "", map[string]any{
		"inputId":    t.inputID,
		"turnNumber": t.sequence,
		"reason":     reason,
		"error":      errorString(cause),
	})
}

func (h *processHandle) emit(eventType, actorKind, actorID string, payload any) {
	h.emitForRun(h.spec.RunID, eventType, actorKind, actorID, payload)
}

func (h *processHandle) emitForRun(runID, eventType, actorKind, actorID string, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		encoded = json.RawMessage(`{"error":"failed to encode event payload"}`)
	}

	h.mu.Lock()
	h.sequence++
	now := time.Now().UTC()
	event := runtimeapi.Event{
		ID:                uuid.NewString(),
		Type:              eventType,
		BoxID:             h.spec.BoxID,
		RunID:             runID,
		RuntimeInstanceID: h.id,
		RuntimeSeq:        h.sequence,
		ActorKind:         actorKind,
		ActorID:           actorID,
		OccurredAt:        now,
		Payload:           encoded,
	}
	h.lastEventAt = now
	h.mu.Unlock()

	h.events <- event
}

func (h *processHandle) waitProcess() {
	<-h.readerDone
	err := h.cmd.Wait()

	h.mu.Lock()
	h.exitErr = err
	h.status = "exited"
	current := h.current
	pending := h.pending
	h.current = nil
	h.pending = nil
	protocolErr := h.protocolErr
	stopRequested := h.stopRequested
	h.mu.Unlock()

	reason := "process_exited"
	if stopRequested {
		reason = "runtime_stopped"
	}
	if protocolErr != nil {
		reason = "protocol_error"
	}
	if current != nil {
		h.emitForRun(current.runID, events.RunFailed, "daemon", "", map[string]any{
			"inputId":    current.inputID,
			"turnNumber": current.sequence,
			"reason":     reason,
			"error":      errorString(firstError(protocolErr, err)),
		})
	}
	if pending != nil {
		h.emitForRun(pending.runID, events.RunFailed, "daemon", "", map[string]any{
			"inputId":    pending.inputID,
			"turnNumber": pending.sequence,
			"reason":     reason,
			"error":      errorString(firstError(protocolErr, err)),
		})
	}

	h.emit(events.RuntimeExited, "daemon", "", map[string]any{
		"error":         errorString(err),
		"protocolError": errorString(protocolErr),
		"stderr":        h.stderr.String(),
		"stopped":       stopRequested,
	})
	close(h.done)
	close(h.events)
}

func firstError(errors ...error) error {
	for _, err := range errors {
		if err != nil {
			return err
		}
	}
	return nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type tailBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func newTailBuffer(limit int) *tailBuffer {
	return &tailBuffer{limit: limit}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(p)
	if b.limit <= 0 {
		return original, nil
	}
	if len(p) >= b.limit {
		b.data = append(b.data[:0], p[len(p)-b.limit:]...)
		return original, nil
	}
	if overflow := len(b.data) + len(p) - b.limit; overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
	}
	b.data = append(b.data, p...)
	return original, nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(bytes.Clone(b.data))
}
