package omp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"agentbox/internal/runtime"

	"github.com/google/uuid"
)

type commandResult struct {
	response rpcResponse
	err      error
}

type pendingCommand struct {
	command string
	runID   string
	result  chan commandResult
}

type rpcResponse struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Command string          `json:"command"`
	Success *bool           `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   string          `json:"error"`
	Code    string          `json:"code"`
}

func (r rpcResponse) agentInvokedFalse() bool {
	if len(r.Data) == 0 {
		return false
	}
	var data struct {
		AgentInvoked *bool `json:"agentInvoked"`
	}
	return json.Unmarshal(r.Data, &data) == nil && data.AgentInvoked != nil && !*data.AgentInvoked
}

type stateResponse struct {
	SessionFile  string `json:"sessionFile"`
	SessionID    string `json:"sessionId"`
	IsStreaming  bool   `json:"isStreaming"`
	IsCompacting bool   `json:"isCompacting"`
}

type ompHandle struct {
	adapter    *Adapter
	id         string
	spec       runtime.StartSpec
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	stderrPipe io.ReadCloser
	stderr     *tailBuffer

	events chan runtime.Event
	done   chan struct{}
	ready  chan readyFrame

	decoder         *frameDecoder
	protocolVersion int
	maxFrameBytes   int

	readers sync.WaitGroup

	writeMu     sync.Mutex
	stdinMu     sync.Mutex
	stdinClosed bool
	pendingMu   sync.Mutex
	pending     map[string]pendingCommand
	lateRuns    map[string]string
	approvalMu  sync.Mutex
	approvals   map[string]*pendingApproval

	stateMu      sync.RWMutex
	state        runtime.State
	sessionRef   string
	runIDMu      sync.RWMutex
	runID        string
	eventMu      sync.Mutex
	nextEventSeq uint64
	eventsClosed bool
	overflowed   bool

	fatalMu sync.RWMutex
	fatal   error
}

func newHandle(adapter *Adapter, cmd *exec.Cmd, stdin io.WriteCloser, stdout, stderr io.ReadCloser, spec runtime.StartSpec) *ompHandle {
	return &ompHandle{
		adapter:         adapter,
		id:              uuid.NewString(),
		spec:            spec,
		cmd:             cmd,
		stdin:           stdin,
		stdout:          stdout,
		stderrPipe:      stderr,
		stderr:          newTailBuffer(adapter.config.StderrBytes),
		events:          make(chan runtime.Event, adapter.config.EventBuffer),
		done:            make(chan struct{}),
		ready:           make(chan readyFrame, 1),
		decoder:         newFrameDecoder(adapter.config.MaxFrameBytes, adapter.config.MaxReassembledFrameBytes),
		protocolVersion: 1,
		maxFrameBytes:   adapter.config.MaxFrameBytes,
		pending:         make(map[string]pendingCommand),
		lateRuns:        make(map[string]string),
		approvals:       make(map[string]*pendingApproval),
		sessionRef:      spec.SessionRef,
		runID:           spec.RunID,
		state: runtime.State{
			Status:       "starting",
			SessionRef:   spec.SessionRef,
			Capabilities: capabilities,
			StartedAt:    time.Now().UTC(),
		},
	}
}

func (h *ompHandle) ID() string {
	return h.id
}

func (h *ompHandle) SessionRef() string {
	h.stateMu.RLock()
	defer h.stateMu.RUnlock()
	return h.sessionRef
}

func (h *ompHandle) activateRun(runID string) string {
	if runID == "" {
		runID = h.spec.RunID
	}
	h.runIDMu.Lock()
	h.runID = runID
	h.runIDMu.Unlock()
	return runID
}

func (h *ompHandle) currentRunID() string {
	h.runIDMu.RLock()
	defer h.runIDMu.RUnlock()
	if h.runID != "" {
		return h.runID
	}
	return h.spec.RunID
}

func (h *ompHandle) completeRun(runID string) {
	h.pendingMu.Lock()
	for id, pendingRunID := range h.lateRuns {
		if pendingRunID == runID {
			delete(h.lateRuns, id)
		}
	}
	h.pendingMu.Unlock()
}

func (h *ompHandle) promptRunID(id string) string {
	if id != "" {
		h.pendingMu.Lock()
		runID := h.lateRuns[id]
		h.pendingMu.Unlock()
		if runID != "" {
			return runID
		}
	}
	return h.currentRunID()
}

func (h *ompHandle) startReaders() {
	h.readers.Add(2)
	go func() {
		defer h.readers.Done()
		h.readStdout()
	}()
	go func() {
		defer h.readers.Done()
		_, _ = io.CopyBuffer(h.stderr, h.stderrPipe, make([]byte, 32<<10))
	}()
	go h.waitProcess()
}

func (h *ompHandle) readStdout() {
	scanner := bufio.NewScanner(h.stdout)
	initialBuffer := 64 << 10
	if initialBuffer > h.adapter.config.MaxFrameBytes+2 {
		initialBuffer = h.adapter.config.MaxFrameBytes + 2
	}
	scanner.Buffer(make([]byte, initialBuffer), h.adapter.config.MaxFrameBytes+2)

	readySeen := false
	for scanner.Scan() {
		raw, err := h.decoder.consume(scanner.Bytes())
		if err != nil {
			h.fail(err)
			return
		}
		if raw == nil {
			continue
		}

		var header frameHeader
		if err := json.Unmarshal(raw, &header); err != nil {
			h.fail(&Error{Op: "read", Code: codeProtocol, Err: err})
			return
		}
		if !readySeen {
			if header.Type != "ready" {
				h.fail(&Error{Op: "ready", Code: codeProtocol, Err: fmt.Errorf("first OMP frame was %q, not ready", header.Type)})
				return
			}
			ready, err := parseReadyFrame(raw, h.adapter.config.MaxFrameBytes, h.adapter.config.MaxReassembledFrameBytes)
			if err != nil {
				h.fail(err)
				return
			}
			h.decoder.setLimits(ready.MaxFrameBytes, ready.MaxReassembledFrameBytes)
			if ready.MaxFrameBytes < h.maxFrameBytes {
				h.maxFrameBytes = ready.MaxFrameBytes
			}
			readySeen = true
			h.ready <- ready
			continue
		}
		if header.Type == "ready" {
			h.fail(&Error{Op: "ready", Code: codeProtocol, Err: errors.New("received duplicate ready frame")})
			return
		}
		if header.Type == "response" {
			if err := h.routeResponse(raw); err != nil {
				h.fail(err)
				return
			}
			continue
		}
		h.normalizeFrame(raw, "", "")
	}

	if err := scanner.Err(); err != nil {
		code := codeProtocol
		if strings.Contains(err.Error(), "token too long") {
			code = codeFrameTooLarge
		}
		h.fail(&Error{Op: "read", Code: code, Err: err})
		return
	}
	if err := h.decoder.finish(); err != nil {
		h.fail(err)
	}
}

func (h *ompHandle) routeResponse(raw json.RawMessage) error {
	var response rpcResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return &Error{Op: "response", Code: codeProtocol, Err: err}
	}
	if response.Type != "response" || response.Command == "" || response.Success == nil {
		return &Error{Op: "response", Code: codeProtocol, Err: errors.New("malformed command response")}
	}
	if response.ID == "" {
		h.emitNotice("uncorrelated OMP command response", raw)
		return nil
	}

	h.pendingMu.Lock()
	pending, ok := h.pending[response.ID]
	lateRunID := h.lateRuns[response.ID]
	if ok {
		delete(h.pending, response.ID)
	} else if !*response.Success {
		delete(h.lateRuns, response.ID)
	}
	h.pendingMu.Unlock()
	if !ok {
		if !*response.Success && (response.Command == "prompt" || response.Command == "abort_and_prompt") {
			if lateRunID == "" {
				lateRunID = h.currentRunID()
			}
			h.setStatus("ready")
			h.emitForRun(lateRunID, "run.failed", "main_agent", "main", map[string]any{
				"reason":   "prompt_scheduling_failed",
				"error":    response.Error,
				"code":     response.Code,
				"response": raw,
			})
			h.completeRun(lateRunID)
			return nil
		}
		h.emitNotice("late or unknown OMP command response", raw)
		return nil
	}
	if response.Command != pending.command {
		err := &Error{Op: "response", Code: codeProtocol, Command: response.Command, Err: fmt.Errorf("response id %q was registered for command %q", response.ID, pending.command)}
		pending.result <- commandResult{err: err}
		return err
	}
	if response.Command == "prompt" && *response.Success {
		h.pendingMu.Lock()
		h.lateRuns[response.ID] = pending.runID
		h.pendingMu.Unlock()
	}
	if response.Command == "negotiate_protocol" && *response.Success {
		var data struct {
			ProtocolVersion int `json:"protocolVersion"`
		}
		if json.Unmarshal(response.Data, &data) == nil && data.ProtocolVersion == 2 {
			h.decoder.enableChunks()
		}
	}
	pending.result <- commandResult{response: response}
	return nil
}

func (h *ompHandle) waitProcess() {
	waitErr := h.cmd.Wait()
	h.readers.Wait()

	statusBeforeExit := h.snapshotState().Status
	fatal := h.fatalError()
	if fatal == nil && statusBeforeExit != "stopping" {
		exitCause := waitErr
		if exitCause == nil {
			exitCause = errors.New("OMP process exited unexpectedly")
		}
		fatal = &Error{Op: "wait", Code: codeProcessExit, Stderr: h.stderr.String(), Err: exitCause}
		h.setFatal(fatal)
	}

	pendingErr := fatal
	if pendingErr == nil {
		pendingErr = &Error{Op: "wait", Code: codeRuntimeStopped, Stderr: h.stderr.String(), Err: errors.New("OMP process exited")}
	}
	h.failPending(pendingErr)
	_ = h.cancelPendingApprovals("process_exit", false)
	h.setStatus("exited")

	payload := map[string]any{
		"expected": statusBeforeExit == "stopping",
		"stderr":   h.stderr.String(),
	}
	if waitErr != nil {
		payload["error"] = waitErr.Error()
	}
	if fatal != nil {
		payload["protocolError"] = fatal.Error()
		payload["errorCode"] = errorCode(fatal)
	}
	if h.cmd.ProcessState != nil {
		payload["exitCode"] = h.cmd.ProcessState.ExitCode()
	}
	h.emit("runtime.exited", "daemon", "", payload)
	h.closeEvents()
	close(h.done)
}

func (h *ompHandle) awaitReady(ctx context.Context, timeout time.Duration) (readyFrame, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case ready := <-h.ready:
		return ready, nil
	case <-h.done:
		if err := h.fatalError(); err != nil {
			return readyFrame{}, err
		}
		return readyFrame{}, &Error{Op: "ready", Code: codeProcessExit, Stderr: h.stderr.String(), Err: errors.New("OMP exited before ready")}
	case <-ctx.Done():
		return readyFrame{}, ctx.Err()
	case <-timer.C:
		return readyFrame{}, &Error{Op: "ready", Code: codeReadyTimeout, Err: fmt.Errorf("OMP did not become ready within %s", timeout)}
	}
}

func (h *ompHandle) sendCommand(ctx context.Context, command string, fields map[string]any) (rpcResponse, error) {
	return h.sendCommandWithPrefix(ctx, "", h.currentRunID(), command, fields)
}

func (h *ompHandle) sendCommandWithPrefix(ctx context.Context, prefix, runID, command string, fields map[string]any) (rpcResponse, error) {
	if err := h.fatalError(); err != nil {
		return rpcResponse{}, err
	}
	select {
	case <-h.done:
		return rpcResponse{}, &Error{Op: "send", Code: codeRuntimeStopped, Command: command, Stderr: h.stderr.String(), Err: errors.New("OMP process has exited")}
	default:
	}
	if err := ctx.Err(); err != nil {
		return rpcResponse{}, err
	}

	id := uuid.NewString()
	if prefix != "" {
		id = prefix + ":" + id
	}
	request := make(map[string]any, len(fields))
	for key, value := range fields {
		request[key] = value
	}
	request["id"] = id
	request["type"] = command

	result := make(chan commandResult, 1)
	h.pendingMu.Lock()
	if _, exists := h.pending[id]; exists {
		h.pendingMu.Unlock()
		return rpcResponse{}, &Error{Op: "send", Code: codeProtocol, Command: command, Err: errors.New("generated duplicate command id")}
	}
	h.pending[id] = pendingCommand{command: command, runID: runID, result: result}
	h.pendingMu.Unlock()

	if err := h.writeRequest(request); err != nil {
		h.removePending(id)
		return rpcResponse{}, err
	}

	select {
	case commandResult := <-result:
		if commandResult.err != nil {
			return rpcResponse{}, commandResult.err
		}
		response := commandResult.response
		if response.Success == nil || !*response.Success {
			message := response.Error
			if message == "" {
				message = "OMP rejected the command"
			}
			return rpcResponse{}, &Error{Op: "send", Code: codeCommandFailed, Command: command, Stderr: h.stderr.String(), Err: errors.New(message)}
		}
		return response, nil
	case <-ctx.Done():
		h.removePending(id)
		return rpcResponse{}, &Error{Op: "send", Code: codeCommandFailed, Command: command, Err: ctx.Err()}
	case <-h.done:
		h.removePending(id)
		if err := h.fatalError(); err != nil {
			return rpcResponse{}, err
		}
		return rpcResponse{}, &Error{Op: "send", Code: codeRuntimeStopped, Command: command, Stderr: h.stderr.String(), Err: errors.New("OMP exited while command was pending")}
	}
}

func (h *ompHandle) writeRequest(request map[string]any) error {
	frame, err := json.Marshal(request)
	if err != nil {
		return &Error{Op: "send", Code: codeProtocol, Err: fmt.Errorf("encode command: %w", err)}
	}
	if len(frame) > h.maxFrameBytes {
		return &Error{Op: "send", Code: codeCommandTooLarge, Err: fmt.Errorf("command is %d bytes; limit is %d", len(frame), h.maxFrameBytes)}
	}
	frame = append(frame, '\n')

	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	if h.isStdinClosed() {
		return &Error{Op: "send", Code: codeRuntimeStopped, Err: errors.New("OMP stdin is closed")}
	}
	if _, err := h.stdin.Write(frame); err != nil {
		return &Error{Op: "send", Code: codeProcessExit, Stderr: h.stderr.String(), Err: err}
	}
	return nil
}

func (h *ompHandle) removePending(id string) {
	h.pendingMu.Lock()
	delete(h.pending, id)
	h.pendingMu.Unlock()
}

func (h *ompHandle) failPending(err error) {
	h.pendingMu.Lock()
	pending := h.pending
	h.pending = make(map[string]pendingCommand)
	h.pendingMu.Unlock()
	for _, command := range pending {
		command.result <- commandResult{err: err}
	}
}

func (h *ompHandle) closeStdin() error {
	h.stdinMu.Lock()
	defer h.stdinMu.Unlock()
	if h.stdinClosed {
		return nil
	}
	h.stdinClosed = true
	return h.stdin.Close()
}

func (h *ompHandle) isStdinClosed() bool {
	h.stdinMu.Lock()
	defer h.stdinMu.Unlock()
	return h.stdinClosed
}

func (h *ompHandle) fail(err error) {
	if err == nil {
		return
	}
	if !h.setFatal(err) {
		return
	}
	_ = h.cancelPendingApprovals("runtime_failure", false)
	h.failPending(err)
	_ = h.closeStdin()
	_ = terminateProcessGroup(h.cmd, true)
}

func (h *ompHandle) setFatal(err error) bool {
	h.fatalMu.Lock()
	defer h.fatalMu.Unlock()
	if h.fatal != nil {
		return false
	}
	h.fatal = err
	return true
}

func (h *ompHandle) fatalError() error {
	h.fatalMu.RLock()
	defer h.fatalMu.RUnlock()
	return h.fatal
}

func (h *ompHandle) applyStateResponse(data json.RawMessage) error {
	if len(data) == 0 {
		return &Error{Op: "inspect", Code: codeProtocol, Err: errors.New("get_state response has no data")}
	}
	var response stateResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return &Error{Op: "inspect", Code: codeProtocol, Err: fmt.Errorf("decode get_state response: %w", err)}
	}

	sessionRef := response.SessionFile
	if sessionRef == "" {
		sessionRef = response.SessionID
	}
	h.stateMu.Lock()
	if sessionRef != "" {
		h.sessionRef = sessionRef
		h.state.SessionRef = sessionRef
	}
	if response.IsStreaming || response.IsCompacting {
		h.state.Status = "busy"
	} else if h.state.Status != "stopping" && h.state.Status != "exited" {
		h.state.Status = "ready"
	}
	h.stateMu.Unlock()
	return nil
}

func (h *ompHandle) setStatus(status string) {
	h.stateMu.Lock()
	if (h.state.Status == "stopping" || h.state.Status == "exited") && status != "stopping" && status != "exited" {
		h.stateMu.Unlock()
		return
	}
	h.state.Status = status
	h.stateMu.Unlock()
}

func (h *ompHandle) snapshotState() runtime.State {
	h.stateMu.RLock()
	defer h.stateMu.RUnlock()
	return h.state
}

func (h *ompHandle) emit(eventType, actorKind, actorID string, payload any) {
	h.emitForRun(h.currentRunID(), eventType, actorKind, actorID, payload)
}

func (h *ompHandle) emitForRun(runID, eventType, actorKind, actorID string, payload any) {
	if runID == "" {
		runID = h.spec.RunID
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		encoded = json.RawMessage(`{"error":"failed to encode event payload"}`)
	}

	h.eventMu.Lock()
	if h.eventsClosed {
		h.eventMu.Unlock()
		return
	}
	h.nextEventSeq++
	event := runtime.Event{
		ID:                uuid.NewString(),
		Type:              eventType,
		BoxID:             h.spec.BoxID,
		RunID:             runID,
		RuntimeInstanceID: h.id,
		RuntimeSeq:        h.nextEventSeq,
		ActorKind:         actorKind,
		ActorID:           actorID,
		OccurredAt:        time.Now().UTC(),
		Payload:           encoded,
	}

	overflow := false
	select {
	case h.events <- event:
	default:
		if !h.overflowed {
			h.overflowed = true
			overflow = true
			select {
			case <-h.events:
			default:
			}
			h.nextEventSeq++
			noticePayload, _ := json.Marshal(map[string]any{
				"message": "OMP event buffer overflowed; runtime was terminated to avoid silent event loss",
				"limit":   cap(h.events),
			})
			h.events <- runtime.Event{
				ID:                uuid.NewString(),
				Type:              "notice",
				BoxID:             h.spec.BoxID,
				RunID:             runID,
				RuntimeInstanceID: h.id,
				RuntimeSeq:        h.nextEventSeq,
				ActorKind:         "daemon",
				OccurredAt:        time.Now().UTC(),
				Payload:           noticePayload,
			}
		}
	}
	h.eventMu.Unlock()

	h.stateMu.Lock()
	h.state.LastEventAt = event.OccurredAt
	h.stateMu.Unlock()
	if overflow {
		h.fail(&Error{Op: "events", Code: codeEventBufferFull, Err: fmt.Errorf("event buffer capacity %d exceeded", cap(h.events))})
	}
}

func (h *ompHandle) closeEvents() {
	h.eventMu.Lock()
	defer h.eventMu.Unlock()
	if h.eventsClosed {
		return
	}
	h.eventsClosed = true
	close(h.events)
}

type tailBuffer struct {
	mu        sync.Mutex
	limit     int
	data      []byte
	truncated bool
}

func newTailBuffer(limit int) *tailBuffer {
	return &tailBuffer{limit: limit, data: make([]byte, 0, limit)}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	originalLength := len(p)
	if b.limit == 0 {
		b.truncated = b.truncated || originalLength > 0
		return originalLength, nil
	}
	if len(p) >= b.limit {
		b.data = append(b.data[:0], p[len(p)-b.limit:]...)
		b.truncated = true
		return originalLength, nil
	}
	if overflow := len(b.data) + len(p) - b.limit; overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
		b.truncated = true
	}
	b.data = append(b.data, p...)
	return originalLength, nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	value := strings.TrimSpace(string(b.data))
	if b.truncated && value != "" {
		return "[truncated]\n" + value
	}
	return value
}

func processAlreadyDone(err error) bool {
	return errors.Is(err, os.ErrProcessDone)
}

var _ runtime.Handle = (*ompHandle)(nil)
