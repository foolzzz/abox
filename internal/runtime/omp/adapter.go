package omp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"agentbox/internal/runtime"
)

const (
	defaultEventBuffer              = 256
	defaultMaxFrameBytes            = 1 << 20
	defaultMaxReassembledFrameBytes = 64 << 20
	defaultStderrBytes              = 64 << 10
	defaultStartupTimeout           = 15 * time.Second
	defaultCleanupTimeout           = 5 * time.Second
)

var capabilities = runtime.Capabilities{
	Steer:          true,
	FollowUp:       true,
	SubagentEvents: true,
	TodoEvents:     true,
	Resume:         true,
}

// Config controls the local OMP process and the adapter's memory bounds.
type Config struct {
	Binary                   string
	EventBuffer              int
	MaxFrameBytes            int
	MaxReassembledFrameBytes int
	StderrBytes              int
	StartupTimeout           time.Duration
	CleanupTimeout           time.Duration
}

// Adapter manages OMP RPC subprocesses.
type Adapter struct {
	config Config
}

// New constructs an OMP adapter. Zero-valued fields receive production defaults.
func New(config Config) *Adapter {
	if config.Binary == "" {
		config.Binary = "omp"
	}
	if config.EventBuffer <= 0 {
		config.EventBuffer = defaultEventBuffer
	}
	if config.MaxFrameBytes <= 0 {
		config.MaxFrameBytes = defaultMaxFrameBytes
	}
	if config.MaxReassembledFrameBytes <= 0 {
		config.MaxReassembledFrameBytes = defaultMaxReassembledFrameBytes
	}
	if config.StderrBytes <= 0 {
		config.StderrBytes = defaultStderrBytes
	}
	if config.StartupTimeout <= 0 {
		config.StartupTimeout = defaultStartupTimeout
	}
	if config.CleanupTimeout <= 0 {
		config.CleanupTimeout = defaultCleanupTimeout
	}
	return &Adapter{config: config}
}

func (a *Adapter) Name() runtime.Type {
	return runtime.TypeOMP
}

func (a *Adapter) Probe(ctx context.Context) (string, runtime.Capabilities, error) {
	cmd := exec.Command(a.config.Binary, "--version")
	configureProcessGroup(cmd)

	stdout := newTailBuffer(16 << 10)
	stderr := newTailBuffer(a.config.StderrBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return "", capabilities, &Error{Op: "probe", Code: codeProcessStart, Err: err}
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			return "", capabilities, &Error{Op: "probe", Code: codeProcessExit, Stderr: stderr.String(), Err: err}
		}
	case <-ctx.Done():
		_ = terminateProcessGroup(cmd, true)
		cleanupTimer := time.NewTimer(a.config.CleanupTimeout)
		defer cleanupTimer.Stop()
		select {
		case <-done:
			return "", capabilities, &Error{Op: "probe", Code: codeProcessExit, Stderr: stderr.String(), Err: ctx.Err()}
		case <-cleanupTimer.C:
			return "", capabilities, &Error{Op: "probe", Code: codeProcessExit, Stderr: stderr.String(), Err: errors.Join(ctx.Err(), errors.New("timed out waiting for OMP probe cleanup"))}
		}
	}

	version := firstLine(stdout.String())
	if version == "" {
		return "", capabilities, &Error{Op: "probe", Code: codeProtocol, Stderr: stderr.String(), Err: errors.New("OMP returned an empty version")}
	}
	return version, capabilities, nil
}

func (a *Adapter) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Handle, error) {
	workspace, err := validateStartSpec(spec)
	if err != nil {
		return nil, err
	}
	if spec.SystemPromptFile != "" {
		promptFile, err := filepath.Abs(spec.SystemPromptFile)
		if err != nil {
			return nil, &Error{Op: "start", Code: codeInvalidSpec, Err: fmt.Errorf("resolve system prompt: %w", err)}
		}
		spec.SystemPromptFile = promptFile
	}
	subagentLevel, err := normalizeSubagentLevel(spec.SubagentEventMode)
	if err != nil {
		return nil, err
	}

	args := []string{"--mode", "rpc", "--cwd", workspace}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.SystemPromptFile != "" {
		args = append(args, "--append-system-prompt", spec.SystemPromptFile)
	}
	if spec.ApprovalMode != "" {
		args = append(args, "--approval-mode", spec.ApprovalMode)
	}
	if spec.SessionRef != "" {
		args = append(args, "--resume", spec.SessionRef)
	}

	cmd := exec.Command(a.config.Binary, args...)
	cmd.Dir = workspace
	cmd.Env = mergeEnvironment(os.Environ(), spec.Environment)
	configureProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, &Error{Op: "start", Code: codeProcessStart, Err: err}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, &Error{Op: "start", Code: codeProcessStart, Err: err}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, &Error{Op: "start", Code: codeProcessStart, Err: err}
	}

	h := newHandle(a, cmd, stdin, stdout, stderr, spec)
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, &Error{Op: "start", Code: codeProcessStart, Err: err}
	}

	h.emit("runtime.starting", "daemon", "", map[string]any{
		"pid":       cmd.Process.Pid,
		"workspace": workspace,
	})
	h.startReaders()

	ready, err := h.awaitReady(ctx, a.config.StartupTimeout)
	if err != nil {
		return nil, a.cleanupFailedStart(h, err)
	}

	if ready.supports(2) {
		response, err := h.sendCommand(ctx, "negotiate_protocol", map[string]any{"protocolVersion": 2})
		if err != nil {
			return nil, a.cleanupFailedStart(h, err)
		}
		var negotiated struct {
			ProtocolVersion int `json:"protocolVersion"`
		}
		if err := json.Unmarshal(response.Data, &negotiated); err != nil || negotiated.ProtocolVersion != 2 {
			if err == nil {
				err = fmt.Errorf("server selected protocol version %d", negotiated.ProtocolVersion)
			}
			return nil, a.cleanupFailedStart(h, &Error{Op: "negotiate", Code: codeProtocol, Command: "negotiate_protocol", Err: err})
		}
		h.protocolVersion = 2
	}

	if _, err := h.sendCommand(ctx, "set_subagent_subscription", map[string]any{"level": subagentLevel}); err != nil {
		return nil, a.cleanupFailedStart(h, err)
	}

	stateResponse, err := h.sendCommand(ctx, "get_state", nil)
	if err != nil {
		return nil, a.cleanupFailedStart(h, err)
	}
	if err := h.applyStateResponse(stateResponse.Data); err != nil {
		return nil, a.cleanupFailedStart(h, err)
	}

	h.setStatus("ready")
	h.emit("runtime.ready", "daemon", "", map[string]any{
		"protocolVersion":          h.protocolVersion,
		"maxFrameBytes":            ready.MaxFrameBytes,
		"maxReassembledFrameBytes": ready.MaxReassembledFrameBytes,
		"sessionRef":               h.SessionRef(),
	})
	return h, nil
}

func (a *Adapter) Send(ctx context.Context, handle runtime.Handle, input runtime.Input) error {
	h, err := a.ownedHandle(handle)
	if err != nil {
		return err
	}

	var command string
	extra := make(map[string]any)
	if len(input.Payload) > 0 {
		if err := json.Unmarshal(input.Payload, &extra); err != nil || extra == nil {
			if err == nil {
				err = errors.New("payload must be a JSON object")
			}
			return &Error{Op: "send", Code: codeInvalidSpec, Command: string(input.Kind), Err: fmt.Errorf("decode input payload: %w", err)}
		}
		for _, reserved := range []string{"id", "type", "message"} {
			if _, exists := extra[reserved]; exists {
				return &Error{Op: "send", Code: codeInvalidSpec, Command: string(input.Kind), Err: fmt.Errorf("input payload cannot set reserved field %q", reserved)}
			}
		}
	}
	extra["message"] = input.Message
	switch input.Kind {
	case runtime.InputPrompt:
		command = "prompt"
	case runtime.InputSteer:
		command = "steer"
	case runtime.InputFollowUp:
		command = "follow_up"
	case runtime.InputApprovalResponse:
		return &Error{Op: "send", Code: codeUnsupportedInput, Command: string(input.Kind), Err: errors.New("OMP extension UI approval bridging is not enabled by this adapter")}
	default:
		return &Error{Op: "send", Code: codeUnsupportedInput, Command: string(input.Kind), Err: errors.New("unsupported runtime input kind")}
	}
	if input.Message == "" {
		return &Error{Op: "send", Code: codeInvalidSpec, Command: command, Err: errors.New("message is empty")}
	}

	runID := h.activateRun(input.RunID)
	response, err := h.sendCommandWithPrefix(ctx, input.ID, runID, command, extra)
	if err != nil {
		return err
	}
	if command == "prompt" && response.agentInvokedFalse() {
		h.setStatus("ready")
		h.emitForRun(runID, "run.completed", "main_agent", "main", map[string]any{
			"reason":  "local_only_prompt",
			"command": command,
		})
		h.completeRun(runID)
	}
	return nil
}

func (a *Adapter) Interrupt(ctx context.Context, handle runtime.Handle) error {
	h, err := a.ownedHandle(handle)
	if err != nil {
		return err
	}
	_, err = h.sendCommand(ctx, "abort", nil)
	return err
}

func (a *Adapter) Stop(ctx context.Context, handle runtime.Handle, mode runtime.StopMode) error {
	h, err := a.ownedHandle(handle)
	if err != nil {
		return err
	}
	if mode != runtime.StopGraceful && mode != runtime.StopForce {
		return &Error{Op: "stop", Code: codeInvalidSpec, Err: fmt.Errorf("unknown stop mode %q", mode)}
	}

	select {
	case <-h.done:
		return nil
	default:
	}
	var stopErr error
	switch mode {
	case runtime.StopGraceful:
		h.setStatus("stopping")
		if err := h.closeStdin(); err != nil && !errors.Is(err, os.ErrClosed) {
			stopErr = err
		}
	case runtime.StopForce:
		h.setStatus("stopping")
		if err := terminateProcessGroup(h.cmd, true); err != nil && !processAlreadyDone(err) {
			stopErr = err
		}
	default:
		return &Error{Op: "stop", Code: codeInvalidSpec, Err: fmt.Errorf("unknown stop mode %q", mode)}
	}

	select {
	case <-h.done:
		if stopErr != nil {
			return &Error{Op: "stop", Code: codeProcessExit, Stderr: h.stderr.String(), Err: stopErr}
		}
		return nil
	case <-ctx.Done():
		_ = terminateProcessGroup(h.cmd, true)
		timer := time.NewTimer(a.config.CleanupTimeout)
		defer timer.Stop()
		select {
		case <-h.done:
		case <-timer.C:
			stopErr = errors.Join(stopErr, errors.New("timed out waiting for OMP process cleanup"))
		}
		return &Error{Op: "stop", Code: codeProcessExit, Stderr: h.stderr.String(), Err: errors.Join(stopErr, ctx.Err())}
	}
}

func (a *Adapter) Events(handle runtime.Handle) <-chan runtime.Event {
	h, err := a.ownedHandle(handle)
	if err != nil {
		ch := make(chan runtime.Event)
		close(ch)
		return ch
	}
	return h.events
}

func (a *Adapter) Inspect(ctx context.Context, handle runtime.Handle) (runtime.State, error) {
	h, err := a.ownedHandle(handle)
	if err != nil {
		return runtime.State{}, err
	}

	state := h.snapshotState()
	if state.Status == "exited" || state.Status == "stopping" {
		return state, nil
	}

	response, err := h.sendCommand(ctx, "get_state", nil)
	if err != nil {
		return runtime.State{}, err
	}
	if err := h.applyStateResponse(response.Data); err != nil {
		return runtime.State{}, err
	}
	return h.snapshotState(), nil
}

func (a *Adapter) ownedHandle(handle runtime.Handle) (*ompHandle, error) {
	h, ok := handle.(*ompHandle)
	if !ok || h == nil || h.adapter != a {
		return nil, &Error{Op: "handle", Code: codeInvalidHandle, Err: errors.New("handle does not belong to this OMP adapter")}
	}
	return h, nil
}

func (a *Adapter) cleanupFailedStart(h *ompHandle, cause error) error {
	h.fail(cause)
	_ = terminateProcessGroup(h.cmd, true)

	timer := time.NewTimer(a.config.CleanupTimeout)
	defer timer.Stop()
	select {
	case <-h.done:
	case <-timer.C:
		cause = errors.Join(cause, errors.New("timed out waiting for OMP process cleanup"))
	}

	return &Error{Op: "start", Code: startErrorCode(cause), Stderr: h.stderr.String(), Err: cause}
}

func validateStartSpec(spec runtime.StartSpec) (string, error) {
	if spec.BoxID == "" {
		return "", &Error{Op: "start", Code: codeInvalidSpec, Err: errors.New("box ID is required")}
	}
	if spec.Workspace == "" {
		return "", &Error{Op: "start", Code: codeInvalidSpec, Err: errors.New("workspace is required")}
	}
	workspace, err := filepath.Abs(spec.Workspace)
	if err != nil {
		return "", &Error{Op: "start", Code: codeInvalidSpec, Err: fmt.Errorf("resolve workspace: %w", err)}
	}
	info, err := os.Stat(workspace)
	if err != nil {
		return "", &Error{Op: "start", Code: codeInvalidSpec, Err: fmt.Errorf("stat workspace: %w", err)}
	}
	if !info.IsDir() {
		return "", &Error{Op: "start", Code: codeInvalidSpec, Err: errors.New("workspace is not a directory")}
	}
	if spec.SystemPromptFile != "" {
		promptInfo, err := os.Stat(spec.SystemPromptFile)
		if err != nil {
			return "", &Error{Op: "start", Code: codeInvalidSpec, Err: fmt.Errorf("stat system prompt: %w", err)}
		}
		if promptInfo.IsDir() {
			return "", &Error{Op: "start", Code: codeInvalidSpec, Err: errors.New("system prompt path is a directory")}
		}
	}
	return workspace, nil
}

func normalizeSubagentLevel(value string) (string, error) {
	switch value {
	case "", "progress":
		return "progress", nil
	case "off", "events":
		return value, nil
	default:
		return "", &Error{Op: "start", Code: codeInvalidSpec, Err: fmt.Errorf("invalid subagent event mode %q", value)}
	}
}

func mergeEnvironment(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}

	merged := make([]string, 0, len(base)+len(overrides))
	positions := make(map[string]int, len(base))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		positions[key] = len(merged)
		merged = append(merged, entry)
	}
	for key, value := range overrides {
		entry := key + "=" + value
		if position, ok := positions[key]; ok {
			merged[position] = entry
			continue
		}
		positions[key] = len(merged)
		merged = append(merged, entry)
	}
	return merged
}

func firstLine(value string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(value), "\n")
	return strings.TrimSpace(line)
}

func startErrorCode(err error) string {
	if code := errorCode(err); code != "" {
		return code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return codeReadyTimeout
	}
	return codeProcessStart
}

// Ensure the adapter continues to satisfy the shared runtime contract.
var _ runtime.Adapter = (*Adapter)(nil)
