package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"agentbox/internal/events"
	runtimeapi "agentbox/internal/runtime"
	"github.com/google/uuid"
)

type threadResponse struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
	Model string `json:"model"`
}

type rpcError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type rpcEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcResult struct {
	result json.RawMessage
	err    error
}

type processHandle struct {
	adapter    *Adapter
	id         string
	spec       runtimeapi.StartSpec
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	stderrPipe io.ReadCloser
	stderr     *tailBuffer

	events chan runtimeapi.Event
	done   chan struct{}

	readers sync.WaitGroup
	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[string]chan rpcResult

	stateMu            sync.RWMutex
	status             string
	threadID           string
	activeTurnID       string
	lastFinishedTurnID string
	currentRunID       string
	turnFailed         bool
	turnInterrupted    bool
	turnError          string
	startedAt          time.Time
	lastEventAt        time.Time
	protocolErr        error
	exitErr            error
	stopRequested      bool

	eventMu      sync.Mutex
	eventSeq     uint64
	eventsClosed bool
	failOnce     sync.Once
}

func newProcessHandle(adapter *Adapter, cmd *exec.Cmd, stdin io.WriteCloser, stdout, stderr io.ReadCloser, spec runtimeapi.StartSpec) *processHandle {
	return &processHandle{
		adapter:      adapter,
		id:           uuid.NewString(),
		spec:         spec,
		cmd:          cmd,
		stdin:        stdin,
		stdout:       stdout,
		stderrPipe:   stderr,
		stderr:       newTailBuffer(adapter.config.StderrBytes),
		events:       make(chan runtimeapi.Event, adapter.config.EventBuffer),
		done:         make(chan struct{}),
		pending:      make(map[string]chan rpcResult),
		status:       "starting",
		threadID:     spec.SessionRef,
		currentRunID: spec.RunID,
		startedAt:    time.Now().UTC(),
	}
}

func (h *processHandle) ID() string {
	return h.id
}

func (h *processHandle) SessionRef() string {
	h.stateMu.RLock()
	defer h.stateMu.RUnlock()
	return h.threadID
}

func (h *processHandle) initialize(ctx context.Context) error {
	response, err := h.request(ctx, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "agentboxd",
			"title":   "AgentBox Host Daemon",
			"version": "0.1.0",
		},
		"capabilities": map[string]any{
			"experimentalApi": true,
		},
	})
	if err != nil {
		return err
	}
	var initialized struct {
		UserAgent      string `json:"userAgent"`
		PlatformFamily string `json:"platformFamily"`
		PlatformOS     string `json:"platformOs"`
	}
	if err := json.Unmarshal(response, &initialized); err != nil {
		return &Error{Op: "initialize", Code: codeProtocol, Method: "initialize", Err: err}
	}
	if strings.TrimSpace(initialized.UserAgent) == "" {
		return &Error{Op: "initialize", Code: codeProtocol, Method: "initialize", Err: errors.New("initialize response did not include a user agent")}
	}
	return h.notify(ctx, "initialized", nil)
}

func (h *processHandle) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := uuid.NewString()
	wire, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return nil, &Error{Op: "request", Code: codeProtocol, Method: method, Err: fmt.Errorf("encode request: %w", err)}
	}
	if len(wire) > h.adapter.config.MaxMessageBytes {
		return nil, &Error{Op: "request", Code: codeRequestTooLarge, Method: method, Err: fmt.Errorf("request is %d bytes; limit is %d", len(wire), h.adapter.config.MaxMessageBytes)}
	}

	result := make(chan rpcResult, 1)
	h.pendingMu.Lock()
	h.pending[id] = result
	h.pendingMu.Unlock()
	if err := h.writeLine(ctx, wire); err != nil {
		h.removePending(id)
		return nil, &Error{Op: "request", Code: codeProcessExit, Method: method, Err: err}
	}

	select {
	case response := <-result:
		return response.result, response.err
	case <-ctx.Done():
		h.removePending(id)
		return nil, &Error{Op: "request", Code: codeRequestTimeout, Method: method, Err: ctx.Err()}
	case <-h.done:
		h.removePending(id)
		return nil, &Error{Op: "request", Code: codeRuntimeStopped, Method: method, Err: errors.New("codex app-server exited before responding")}
	}
}

func (h *processHandle) notify(ctx context.Context, method string, params any) error {
	message := map[string]any{"method": method}
	if params != nil {
		message["params"] = params
	}
	wire, err := json.Marshal(message)
	if err != nil {
		return &Error{Op: "notify", Code: codeProtocol, Method: method, Err: err}
	}
	if len(wire) > h.adapter.config.MaxMessageBytes {
		return &Error{Op: "notify", Code: codeRequestTooLarge, Method: method, Err: fmt.Errorf("notification is %d bytes; limit is %d", len(wire), h.adapter.config.MaxMessageBytes)}
	}
	return h.writeLine(ctx, wire)
}

func (h *processHandle) writeLine(ctx context.Context, wire []byte) error {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-h.done:
		return errors.New("codex app-server has exited")
	default:
	}
	payload := make([]byte, len(wire)+1)
	copy(payload, wire)
	payload[len(wire)] = '\n'
	for written := 0; written < len(payload); {
		count, err := h.stdin.Write(payload[written:])
		written += count
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func (h *processHandle) removePending(id string) {
	h.pendingMu.Lock()
	delete(h.pending, id)
	h.pendingMu.Unlock()
}

func (h *processHandle) startReaders() {
	h.readers.Add(2)
	go func() {
		defer h.readers.Done()
		if err := h.readOutput(); err != nil {
			h.failProtocol(err)
		}
	}()
	go func() {
		defer h.readers.Done()
		_, _ = io.CopyBuffer(h.stderr, h.stderrPipe, make([]byte, 32<<10))
	}()
	go h.waitProcess()
}

func (h *processHandle) readOutput() error {
	reader := bufio.NewReaderSize(h.stdout, 64<<10)
	var lineNumber uint64
	for {
		line, err := readBoundedLine(reader, h.adapter.config.MaxMessageBytes)
		if len(bytes.TrimSpace(line)) > 0 {
			lineNumber++
			if !utf8.Valid(line) {
				return &Error{Op: "read", Code: codeProtocol, Err: fmt.Errorf("JSON-RPC line %d is not valid UTF-8", lineNumber)}
			}
			if processErr := h.processLine(lineNumber, line); processErr != nil {
				return processErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func readBoundedLine(reader *bufio.Reader, limit int) ([]byte, error) {
	buffer := make([]byte, 0, min(limit, 64<<10))
	for {
		fragment, prefix, err := reader.ReadLine()
		if len(buffer)+len(fragment) > limit {
			return nil, &Error{Op: "read", Code: codeFrameTooLarge, Err: fmt.Errorf("JSON-RPC frame exceeds %d-byte limit", limit)}
		}
		buffer = append(buffer, fragment...)
		if prefix {
			if err != nil {
				return nil, err
			}
			continue
		}
		return buffer, err
	}
}

func (h *processHandle) processLine(lineNumber uint64, line []byte) error {
	var message rpcEnvelope
	if err := json.Unmarshal(line, &message); err != nil {
		return &Error{Op: "read", Code: codeProtocol, Err: fmt.Errorf("decode JSON-RPC line %d: %w", lineNumber, err)}
	}
	if message.Method != "" {
		if len(message.ID) > 0 && string(message.ID) != "null" {
			return h.handleServerRequest(message)
		}
		h.normalizeNotification(message.Method, message.Params)
		return nil
	}
	if len(message.ID) == 0 {
		return &Error{Op: "read", Code: codeProtocol, Err: fmt.Errorf("JSON-RPC line %d has neither method nor id", lineNumber)}
	}
	return h.routeResponse(message)
}

func (h *processHandle) routeResponse(message rpcEnvelope) error {
	id, err := responseID(message.ID)
	if err != nil {
		return &Error{Op: "response", Code: codeProtocol, Err: err}
	}
	h.pendingMu.Lock()
	pending, ok := h.pending[id]
	if ok {
		delete(h.pending, id)
	}
	h.pendingMu.Unlock()
	if !ok {
		h.emit(events.Notice, "daemon", "", map[string]any{"kind": "protocol.late_response"})
		return nil
	}
	if message.Error != nil {
		pending <- rpcResult{err: &Error{
			Op:   "response",
			Code: codeRequestFailed,
			Err:  fmt.Errorf("codex JSON-RPC error %d: %s", message.Error.Code, boundedText(message.Error.Message, 1024)),
		}}
		return nil
	}
	if message.Result == nil {
		pending <- rpcResult{err: &Error{Op: "response", Code: codeProtocol, Err: errors.New("response omitted result")}}
		return nil
	}
	pending <- rpcResult{result: append(json.RawMessage(nil), message.Result...)}
	return nil
}

func responseID(raw json.RawMessage) (string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil && text != "" {
		return text, nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&number) == nil && number.String() != "" {
		return number.String(), nil
	}
	return "", errors.New("response id is neither a non-empty string nor a number")
}

func (h *processHandle) handleServerRequest(message rpcEnvelope) error {
	response := map[string]any{
		"id": json.RawMessage(message.ID),
		"error": map[string]any{
			"code":    -32601,
			"message": "AgentBox does not implement this Codex client callback",
		},
	}
	wire, err := json.Marshal(response)
	if err != nil {
		return &Error{Op: "server_request", Code: codeProtocol, Method: message.Method, Err: err}
	}
	ctx, cancel := context.WithTimeout(context.Background(), h.adapter.config.StopGrace)
	defer cancel()
	if err := h.writeLine(ctx, wire); err != nil {
		return &Error{Op: "server_request", Code: codeProcessExit, Method: message.Method, Err: err}
	}
	h.emit(events.Notice, "daemon", "", map[string]any{
		"kind":   "protocol.unsupported_server_request",
		"method": message.Method,
	})
	return nil
}

func (h *processHandle) failProtocol(err error) {
	h.failOnce.Do(func() {
		h.stateMu.Lock()
		h.protocolErr = err
		h.stateMu.Unlock()
		_ = signalProcessGroup(h.cmd.Process, osKillSignal())
	})
}

func (h *processHandle) waitProcess() {
	waitErr := h.cmd.Wait()
	h.readers.Wait()

	h.stateMu.Lock()
	previousStatus := h.status
	currentRunID := h.currentRunID
	protocolErr := h.protocolErr
	stopRequested := h.stopRequested
	h.exitErr = waitErr
	h.status = "exited"
	h.activeTurnID = ""
	h.stateMu.Unlock()

	cause := protocolErr
	if cause == nil && waitErr != nil {
		cause = waitErr
	}
	h.failPending(&Error{Op: "wait", Code: codeRuntimeStopped, Err: firstError(cause, errors.New("codex app-server exited"))})
	if currentRunID != "" && previousStatus == "busy" {
		h.emitForRun(currentRunID, events.RunFailed, "daemon", "", map[string]any{
			"reason": "runtime_exited",
			"error":  errorText(cause),
		})
	}
	h.emit(events.RuntimeExited, "daemon", "", map[string]any{
		"reason":           exitReason(stopRequested, protocolErr),
		"expected":         stopRequested,
		"exitCode":         processExitCode(h.cmd),
		"diagnosticOutput": h.stderr.Len() > 0,
	})

	h.eventMu.Lock()
	h.eventsClosed = true
	close(h.events)
	h.eventMu.Unlock()
	close(h.done)
}

func (h *processHandle) failPending(err error) {
	h.pendingMu.Lock()
	pending := h.pending
	h.pending = make(map[string]chan rpcResult)
	h.pendingMu.Unlock()
	for _, result := range pending {
		result <- rpcResult{err: err}
	}
}

func (h *processHandle) stop(ctx context.Context, mode runtimeapi.StopMode) error {
	select {
	case <-h.done:
		return nil
	default:
	}
	h.stateMu.Lock()
	h.stopRequested = true
	if h.status != "exited" {
		h.status = "stopping"
	}
	h.stateMu.Unlock()
	_ = h.stdin.Close()

	signal := gracefulStopSignal()
	if mode == runtimeapi.StopForce {
		signal = osKillSignal()
	}
	if err := signalProcessGroup(h.cmd.Process, signal); err != nil {
		return &Error{Op: "stop", Code: codeProcessExit, Err: err}
	}
	if mode == runtimeapi.StopForce {
		select {
		case <-h.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	timer := time.NewTimer(h.adapter.config.StopGrace)
	defer timer.Stop()
	select {
	case <-h.done:
		return nil
	case <-ctx.Done():
		_ = signalProcessGroup(h.cmd.Process, osKillSignal())
		return ctx.Err()
	case <-timer.C:
		if err := signalProcessGroup(h.cmd.Process, osKillSignal()); err != nil {
			return &Error{Op: "stop", Code: codeProcessExit, Err: err}
		}
		select {
		case <-h.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (h *processHandle) setThread(threadID string) {
	h.stateMu.Lock()
	h.threadID = threadID
	h.stateMu.Unlock()
}

func (h *processHandle) setStatus(status string) {
	h.stateMu.Lock()
	h.status = status
	h.stateMu.Unlock()
}

func (h *processHandle) prepareTurn(runID string) (string, string, error) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	if h.status == "exited" || h.status == "stopping" {
		return "", "", &Error{Op: "send", Code: codeRuntimeStopped, Err: errors.New("codex runtime is not active")}
	}
	if h.threadID == "" {
		return "", "", &Error{Op: "send", Code: codeProtocol, Err: errors.New("codex thread id is unavailable")}
	}
	active := h.activeTurnID
	if active == "" {
		h.currentRunID = runID
		h.turnFailed = false
		h.turnInterrupted = false
		h.turnError = ""
		h.status = "busy"
	}
	return h.threadID, active, nil
}

func (h *processHandle) revertPreparedTurn(runID string) {
	h.stateMu.Lock()
	if h.activeTurnID == "" && h.currentRunID == runID && h.status == "busy" {
		h.status = "ready"
		h.currentRunID = ""
	}
	h.stateMu.Unlock()
}

func (h *processHandle) prepareSteer(runID string) (string, string, error) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	if h.status == "exited" || h.status == "stopping" {
		return "", "", &Error{Op: "steer", Code: codeRuntimeStopped, Err: errors.New("codex runtime is not active")}
	}
	if h.threadID == "" || h.activeTurnID == "" {
		return "", "", &Error{Op: "steer", Code: codeRequestFailed, Err: errors.New("codex has no active turn to steer")}
	}
	if runID != "" {
		h.currentRunID = runID
	}
	return h.threadID, h.activeTurnID, nil
}

func (h *processHandle) activateTurn(turnID, runID string) {
	h.stateMu.Lock()
	if turnID != "" && h.lastFinishedTurnID == turnID {
		h.stateMu.Unlock()
		return
	}
	if h.activeTurnID != turnID {
		h.turnFailed = false
		h.turnInterrupted = false
		h.turnError = ""
	}
	h.activeTurnID = turnID
	h.currentRunID = runID
	h.status = "busy"
	h.stateMu.Unlock()
}

func (h *processHandle) prepareInterrupt() (string, string) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	if h.activeTurnID != "" {
		h.turnInterrupted = true
	}
	return h.threadID, h.activeTurnID
}

func (h *processHandle) revertInterrupt(turnID string) {
	h.stateMu.Lock()
	if h.activeTurnID == turnID {
		h.turnInterrupted = false
	}
	h.stateMu.Unlock()
}

func (h *processHandle) currentRun() string {
	h.stateMu.RLock()
	defer h.stateMu.RUnlock()
	if h.currentRunID != "" {
		return h.currentRunID
	}
	return h.spec.RunID
}

func (h *processHandle) snapshot() runtimeapi.State {
	h.stateMu.RLock()
	defer h.stateMu.RUnlock()
	return runtimeapi.State{
		Status:       h.status,
		SessionRef:   h.threadID,
		Capabilities: adapterCapabilities,
		StartedAt:    h.startedAt,
		LastEventAt:  h.lastEventAt,
	}
}

func (h *processHandle) emit(eventType, actorKind, actorID string, payload any) {
	h.emitForRun(h.currentRun(), eventType, actorKind, actorID, payload)
}

func (h *processHandle) emitForRun(runID, eventType, actorKind, actorID string, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		encoded = json.RawMessage(`{"error":"failed to encode event payload"}`)
	}
	now := time.Now().UTC()
	h.eventMu.Lock()
	if h.eventsClosed {
		h.eventMu.Unlock()
		return
	}
	h.eventSeq++
	event := runtimeapi.Event{
		ID:                uuid.NewString(),
		Type:              eventType,
		BoxID:             h.spec.BoxID,
		RunID:             runID,
		RuntimeInstanceID: h.id,
		RuntimeSeq:        h.eventSeq,
		ActorKind:         actorKind,
		ActorID:           actorID,
		OccurredAt:        now,
		Payload:           encoded,
	}
	select {
	case h.events <- event:
		h.eventMu.Unlock()
		h.stateMu.Lock()
		h.lastEventAt = now
		h.stateMu.Unlock()
	default:
		h.eventMu.Unlock()
		h.failProtocol(&Error{Op: "emit", Code: codeEventBufferFull, Err: fmt.Errorf("event buffer capacity %d was exhausted", cap(h.events))})
	}
}

func boundedText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

func exitReason(stopped bool, protocolErr error) string {
	if stopped {
		return "runtime_stopped"
	}
	if protocolErr != nil {
		return "protocol_error"
	}
	return "process_exited"
}

func processExitCode(cmd *exec.Cmd) int {
	if cmd.ProcessState == nil {
		return -1
	}
	return cmd.ProcessState.ExitCode()
}

func firstError(values ...error) error {
	for _, err := range values {
		if err != nil {
			return err
		}
	}
	return nil
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return boundedText(err.Error(), 2048)
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

func (b *tailBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.data)
}
