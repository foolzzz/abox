package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agentbox/internal/events"
	runtimeapi "agentbox/internal/runtime"
)

const (
	defaultBinary          = "codex"
	defaultMaxMessageBytes = 8 << 20
	defaultMaxPromptBytes  = 1 << 20
	defaultStderrBytes     = 64 << 10
	defaultEventBuffer     = 256
	defaultStartupTimeout  = 20 * time.Second
	defaultStopGrace       = 5 * time.Second
	probeOutputBytes       = 16 << 10
)

var adapterCapabilities = runtimeapi.Capabilities{
	Steer:          true,
	FollowUp:       true,
	SubagentEvents: true,
	TodoEvents:     true,
	Resume:         true,
}

// Config controls the local Codex app-server process and protocol bounds.
type Config struct {
	Binary          string
	MaxMessageBytes int
	MaxPromptBytes  int
	StderrBytes     int
	EventBuffer     int
	StartupTimeout  time.Duration
	StopGrace       time.Duration
}

// Adapter manages Codex app-server subprocesses using its JSON-RPC protocol.
type Adapter struct {
	config Config
}

func New(config Config) *Adapter {
	if strings.TrimSpace(config.Binary) == "" {
		config.Binary = defaultBinary
	}
	if config.MaxMessageBytes <= 0 {
		config.MaxMessageBytes = defaultMaxMessageBytes
	}
	if config.MaxPromptBytes <= 0 {
		config.MaxPromptBytes = defaultMaxPromptBytes
	}
	if config.StderrBytes <= 0 {
		config.StderrBytes = defaultStderrBytes
	}
	if config.EventBuffer <= 0 {
		config.EventBuffer = defaultEventBuffer
	}
	if config.StartupTimeout <= 0 {
		config.StartupTimeout = defaultStartupTimeout
	}
	if config.StopGrace <= 0 {
		config.StopGrace = defaultStopGrace
	}
	return &Adapter{config: config}
}

func (a *Adapter) Name() runtimeapi.Type {
	return runtimeapi.TypeCodex
}

func (a *Adapter) Probe(ctx context.Context) (string, runtimeapi.Capabilities, error) {
	version, err := a.probeVersion(ctx)
	if err != nil {
		return "", adapterCapabilities, err
	}

	h, err := a.startProcess(runtimeapi.StartSpec{})
	if err != nil {
		return version, adapterCapabilities, err
	}
	probeCtx, cancel := boundedContext(ctx, a.config.StartupTimeout)
	defer cancel()
	defer a.stopFailedStart(h)

	if err := h.initialize(probeCtx); err != nil {
		return version, adapterCapabilities, &Error{Op: "probe", Code: codeProtocol, Err: err}
	}
	response, err := h.request(probeCtx, "account/read", map[string]any{"refreshToken": false})
	if err != nil {
		return version, adapterCapabilities, &Error{Op: "probe", Code: errorCodeOr(codeRequestFailed, err), Method: "account/read", Err: err}
	}
	var account struct {
		Account            json.RawMessage `json:"account"`
		RequiresOpenAIAuth bool            `json:"requiresOpenaiAuth"`
	}
	if err := json.Unmarshal(response, &account); err != nil {
		return version, adapterCapabilities, &Error{Op: "probe", Code: codeProtocol, Method: "account/read", Err: fmt.Errorf("decode account status: %w", err)}
	}
	if account.RequiresOpenAIAuth && isJSONNull(account.Account) {
		return version, adapterCapabilities, &Error{Op: "probe", Code: codeAuthentication, Err: errors.New("Codex is not logged in; run codex login as the daemon system user")}
	}
	return version, adapterCapabilities, nil
}

func (a *Adapter) probeVersion(ctx context.Context) (string, error) {
	cmd := exec.Command(a.config.Binary, "--version")
	configureProcessGroup(cmd)
	stdout := newTailBuffer(probeOutputBytes)
	stderr := newTailBuffer(a.config.StderrBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return "", &Error{Op: "probe", Code: codeProcessStart, Err: err}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return "", &Error{Op: "probe", Code: codeProcessExit, Err: err}
		}
	case <-ctx.Done():
		_ = signalProcessGroup(cmd.Process, osKillSignal())
		select {
		case <-done:
		case <-time.After(a.config.StopGrace):
			return "", &Error{Op: "probe", Code: codeProcessExit, Err: errors.Join(ctx.Err(), errors.New("timed out waiting for Codex version probe cleanup"))}
		}
		return "", &Error{Op: "probe", Code: codeRequestTimeout, Err: ctx.Err()}
	}
	version := firstLine(stdout.String())
	if version == "" {
		return "", &Error{Op: "probe", Code: codeProtocol, Err: errors.New("Codex returned an empty version")}
	}
	return version, nil
}

func (a *Adapter) Start(ctx context.Context, spec runtimeapi.StartSpec) (runtimeapi.Handle, error) {
	workspace, prompt, err := a.validateStartSpec(spec)
	if err != nil {
		return nil, err
	}
	spec.Workspace = workspace

	h, err := a.startProcess(spec)
	if err != nil {
		return nil, err
	}
	startupCtx, cancel := boundedContext(ctx, a.config.StartupTimeout)
	defer cancel()
	if err := h.initialize(startupCtx); err != nil {
		return nil, a.cleanupFailedStart(h, err)
	}

	params := map[string]any{
		"cwd":            workspace,
		"approvalPolicy": "never",
		"sandbox":        "workspace-write",
	}
	if spec.Model != "" {
		params["model"] = spec.Model
	}
	if prompt != "" {
		params["developerInstructions"] = prompt
	}

	method := "thread/start"
	if spec.SessionRef == "" {
		params["ephemeral"] = false
	} else {
		method = "thread/resume"
		params["threadId"] = spec.SessionRef
		params["excludeTurns"] = true
	}
	response, err := h.request(startupCtx, method, params)
	if err != nil {
		return nil, a.cleanupFailedStart(h, err)
	}
	var started threadResponse
	if err := json.Unmarshal(response, &started); err != nil {
		return nil, a.cleanupFailedStart(h, &Error{Op: "start", Code: codeProtocol, Method: method, Err: fmt.Errorf("decode thread response: %w", err)})
	}
	if strings.TrimSpace(started.Thread.ID) == "" {
		return nil, a.cleanupFailedStart(h, &Error{Op: "start", Code: codeProtocol, Method: method, Err: errors.New("thread response did not include a thread id")})
	}
	h.setThread(started.Thread.ID)
	h.setStatus("ready")
	h.emit(events.RuntimeReady, "daemon", "", map[string]any{
		"sessionRef": started.Thread.ID,
		"model":      started.Model,
		"resumed":    spec.SessionRef != "",
		"transport":  "app-server-json-rpc",
	})
	return h, nil
}

func (a *Adapter) startProcess(spec runtimeapi.StartSpec) (*processHandle, error) {
	if err := validateEnvironmentOverrides(spec.Environment); err != nil {
		return nil, err
	}
	cmd := exec.Command(a.config.Binary, "app-server", "--listen", "stdio://")
	if spec.Workspace != "" {
		cmd.Dir = spec.Workspace
	}
	cmd.Env = mergedEnvironment(spec.Environment)
	configureProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, &Error{Op: "start", Code: codeProcessStart, Err: fmt.Errorf("open stdin: %w", err)}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, &Error{Op: "start", Code: codeProcessStart, Err: fmt.Errorf("open stdout: %w", err)}
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, &Error{Op: "start", Code: codeProcessStart, Err: fmt.Errorf("open stderr: %w", err)}
	}

	h := newProcessHandle(a, cmd, stdin, stdout, stderrPipe, spec)
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderrPipe.Close()
		return nil, &Error{Op: "start", Code: codeProcessStart, Err: err}
	}
	h.emit(events.RuntimeStarting, "daemon", "", map[string]any{
		"pid":       cmd.Process.Pid,
		"workspace": spec.Workspace,
		"transport": "app-server-json-rpc",
	})
	h.startReaders()
	return h, nil
}

func (a *Adapter) Send(ctx context.Context, handle runtimeapi.Handle, input runtimeapi.Input) error {
	h, err := a.ownedHandle(handle)
	if err != nil {
		return err
	}
	if strings.TrimSpace(input.Message) == "" {
		return &Error{Op: "send", Code: codeInvalidSpec, Err: errors.New("message is empty")}
	}
	if len(input.Payload) > 0 {
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(input.Payload, &payload); err != nil || payload == nil {
			return &Error{Op: "send", Code: codeInvalidSpec, Err: errors.New("Codex input payload must be a JSON object")}
		}
		if len(payload) != 0 {
			return &Error{Op: "send", Code: codeUnsupportedInput, Err: errors.New("Codex text input does not accept an auxiliary payload")}
		}
	}

	content := []map[string]any{{"type": "text", "text": input.Message}}
	runID := input.RunID
	if runID == "" {
		runID = h.spec.RunID
	}
	switch input.Kind {
	case runtimeapi.InputPrompt, runtimeapi.InputFollowUp:
		threadID, activeTurn, err := h.prepareTurn(runID)
		if err != nil {
			return err
		}
		if activeTurn != "" {
			return &Error{Op: "send", Code: codeRequestFailed, Method: "turn/start", Err: errors.New("a Codex turn is already active; use steer or interrupt before starting a follow-up")}
		}
		params := map[string]any{"threadId": threadID, "input": content}
		if input.ID != "" {
			params["clientUserMessageId"] = input.ID
		}
		response, err := h.request(ctx, "turn/start", params)
		if err != nil {
			h.revertPreparedTurn(runID)
			return err
		}
		var result struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if err := json.Unmarshal(response, &result); err != nil || strings.TrimSpace(result.Turn.ID) == "" {
			if err == nil {
				err = errors.New("turn response did not include a turn id")
			}
			h.revertPreparedTurn(runID)
			return &Error{Op: "send", Code: codeProtocol, Method: "turn/start", Err: err}
		}
		h.activateTurn(result.Turn.ID, runID)
		return nil
	case runtimeapi.InputSteer:
		threadID, activeTurn, err := h.prepareSteer(runID)
		if err != nil {
			return err
		}
		params := map[string]any{
			"threadId":       threadID,
			"expectedTurnId": activeTurn,
			"input":          content,
		}
		if input.ID != "" {
			params["clientUserMessageId"] = input.ID
		}
		_, err = h.request(ctx, "turn/steer", params)
		return err
	case runtimeapi.InputApprovalResponse:
		return &Error{Op: "send", Code: codeUnsupportedInput, Err: errors.New("Codex interactive approvals are disabled; the adapter runs with approvalPolicy=never")}
	default:
		return &Error{Op: "send", Code: codeUnsupportedInput, Err: fmt.Errorf("unsupported input kind %q", input.Kind)}
	}
}

func (a *Adapter) Interrupt(ctx context.Context, handle runtimeapi.Handle) error {
	h, err := a.ownedHandle(handle)
	if err != nil {
		return err
	}
	threadID, turnID := h.prepareInterrupt()
	if turnID == "" {
		return nil
	}
	_, err = h.request(ctx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID})
	if err != nil {
		h.revertInterrupt(turnID)
	}
	return err
}

func (a *Adapter) Stop(ctx context.Context, handle runtimeapi.Handle, mode runtimeapi.StopMode) error {
	h, err := a.ownedHandle(handle)
	if err != nil {
		return err
	}
	if mode != runtimeapi.StopGraceful && mode != runtimeapi.StopForce {
		return &Error{Op: "stop", Code: codeInvalidSpec, Err: fmt.Errorf("unknown stop mode %q", mode)}
	}
	return h.stop(ctx, mode)
}

func (a *Adapter) Events(handle runtimeapi.Handle) <-chan runtimeapi.Event {
	h, err := a.ownedHandle(handle)
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
	h, err := a.ownedHandle(handle)
	if err != nil {
		return runtimeapi.State{}, err
	}
	return h.snapshot(), nil
}

func (a *Adapter) ownedHandle(handle runtimeapi.Handle) (*processHandle, error) {
	h, ok := handle.(*processHandle)
	if !ok || h == nil || h.adapter != a {
		return nil, &Error{Op: "handle", Code: codeInvalidHandle, Err: errors.New("handle does not belong to this Codex adapter")}
	}
	return h, nil
}

func (a *Adapter) validateStartSpec(spec runtimeapi.StartSpec) (string, string, error) {
	if strings.TrimSpace(spec.BoxID) == "" {
		return "", "", &Error{Op: "start", Code: codeInvalidSpec, Err: errors.New("box ID is required")}
	}
	if strings.TrimSpace(spec.Workspace) == "" {
		return "", "", &Error{Op: "start", Code: codeInvalidSpec, Err: errors.New("workspace is required")}
	}
	workspace, err := filepath.Abs(spec.Workspace)
	if err != nil {
		return "", "", &Error{Op: "start", Code: codeInvalidSpec, Err: fmt.Errorf("resolve workspace: %w", err)}
	}
	info, err := os.Stat(workspace)
	if err != nil {
		return "", "", &Error{Op: "start", Code: codeInvalidSpec, Err: fmt.Errorf("stat workspace: %w", err)}
	}
	if !info.IsDir() {
		return "", "", &Error{Op: "start", Code: codeInvalidSpec, Err: errors.New("workspace is not a directory")}
	}
	if err := validateEnvironmentOverrides(spec.Environment); err != nil {
		return "", "", err
	}
	approvalMode := strings.TrimSpace(spec.ApprovalMode)
	if approvalMode != "" && approvalMode != "never" && approvalMode != "dontAsk" && approvalMode != "dont-ask" {
		return "", "", &Error{Op: "start", Code: codeInvalidSpec, Err: fmt.Errorf("approval mode %q is unsupported; Codex V1 runs non-interactively with approvalPolicy=never", approvalMode)}
	}
	prompt, err := a.readSystemPrompt(spec.SystemPromptFile)
	if err != nil {
		return "", "", err
	}
	return workspace, prompt, nil
}

func (a *Adapter) readSystemPrompt(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", &Error{Op: "start", Code: codeInvalidSpec, Err: fmt.Errorf("open system prompt: %w", err)}
	}
	defer file.Close()
	limited := io.LimitReader(file, int64(a.config.MaxPromptBytes)+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		return "", &Error{Op: "start", Code: codeInvalidSpec, Err: fmt.Errorf("read system prompt: %w", err)}
	}
	if len(content) > a.config.MaxPromptBytes {
		return "", &Error{Op: "start", Code: codeRequestTooLarge, Err: fmt.Errorf("system prompt exceeds %d-byte limit", a.config.MaxPromptBytes)}
	}
	return string(content), nil
}

func (a *Adapter) cleanupFailedStart(h *processHandle, cause error) error {
	a.stopFailedStart(h)
	return &Error{Op: "start", Code: errorCodeOr(codeProcessStart, cause), Err: cause}
}

func (a *Adapter) stopFailedStart(h *processHandle) {
	ctx, cancel := context.WithTimeout(context.Background(), a.config.StopGrace)
	defer cancel()
	_ = h.stop(ctx, runtimeapi.StopForce)
}

func validateEnvironmentOverrides(overrides map[string]string) error {
	if len(overrides) != 0 {
		return &Error{Op: "start", Code: codeInvalidSpec, Err: errors.New("Codex V1 does not accept remote environment overrides; authentication and configuration come from the daemon system user")}
	}
	return nil
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
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+values[name])
	}
	return result
}

func boundedContext(parent context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) <= limit {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, limit)
}

func isJSONNull(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed == "" || trimmed == "null"
}

func errorCodeOr(fallback string, err error) string {
	if code := errorCode(err); code != "" {
		return code
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return codeRequestTimeout
	}
	return fallback
}

func firstLine(value string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(value), "\n")
	return strings.TrimSpace(line)
}

var _ runtimeapi.Adapter = (*Adapter)(nil)
var _ runtimeapi.Handle = (*processHandle)(nil)
