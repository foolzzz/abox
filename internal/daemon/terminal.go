package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"unicode"

	hostv1 "agentbox/api"
	"github.com/creack/pty"
)

const (
	terminalModeCommand = "command"
	terminalModeAgent   = "agent"
)

type terminalSession struct {
	id          string
	boxID       string
	mode        string
	tmuxSession string
	command     *exec.Cmd
	pty         *os.File
	cancel      context.CancelFunc
	once        sync.Once
}

type TerminalManager struct {
	guard       *WorkspaceGuard
	tmuxBinary  string
	maxSessions int
	emit        func(*hostv1.TerminalData) error

	mu       sync.Mutex
	sessions map[string]*terminalSession
}

func NewTerminalManager(guard *WorkspaceGuard, tmuxBinary string, maxSessions int, emit func(*hostv1.TerminalData) error) (*TerminalManager, error) {
	if guard == nil || emit == nil {
		return nil, errors.New("workspace guard and terminal emitter are required")
	}
	if maxSessions <= 0 {
		return nil, errors.New("terminal session limit must be positive")
	}
	resolvedTmux, err := exec.LookPath(strings.TrimSpace(tmuxBinary))
	if err != nil {
		return nil, fmt.Errorf("resolve tmux binary: %w", err)
	}
	return &TerminalManager{
		guard:       guard,
		tmuxBinary:  resolvedTmux,
		maxSessions: maxSessions,
		emit:        emit,
		sessions:    make(map[string]*terminalSession),
	}, nil
}

func (m *TerminalManager) HandleTerminal(ctx context.Context, input *hostv1.TerminalInput) error {
	if input == nil {
		return errors.New("terminal input is required")
	}
	if input.GetTerminate() {
		if strings.TrimSpace(input.GetBoxId()) == "" {
			return errors.New("box ID is required to terminate an Agent Terminal")
		}
		return m.terminateAgentSession(ctx, input.GetBoxId())
	}
	if input.GetSessionId() == "" {
		return errors.New("terminal session ID is required")
	}
	if input.GetOpen() {
		return m.open(ctx, input)
	}
	m.mu.Lock()
	session := m.sessions[input.GetSessionId()]
	m.mu.Unlock()
	if session == nil {
		return fmt.Errorf("terminal session %q is not active", input.GetSessionId())
	}
	if input.GetColumns() > 0 && input.GetRows() > 0 {
		if err := pty.Setsize(session.pty, &pty.Winsize{Cols: uint16(input.GetColumns()), Rows: uint16(input.GetRows())}); err != nil {
			return fmt.Errorf("resize terminal: %w", err)
		}
	}
	if len(input.GetData()) > 0 {
		if _, err := session.pty.Write(input.GetData()); err != nil {
			return fmt.Errorf("write terminal input: %w", err)
		}
	}
	if input.GetClose() {
		m.closeSession(session, true)
	}
	return nil
}

func (m *TerminalManager) open(parent context.Context, input *hostv1.TerminalInput) error {
	workspace, err := m.guard.ResolveWorkspace(input.GetWorkspace())
	if err != nil {
		return fmt.Errorf("reject terminal workspace: %w", err)
	}
	mode := strings.TrimSpace(input.GetMode())
	if mode == "" {
		mode = terminalModeCommand
	}
	if mode != terminalModeCommand && mode != terminalModeAgent {
		return fmt.Errorf("unsupported terminal mode %q", mode)
	}
	m.mu.Lock()
	if _, exists := m.sessions[input.GetSessionId()]; exists {
		m.mu.Unlock()
		return fmt.Errorf("terminal connection %q already exists", input.GetSessionId())
	}
	if len(m.sessions) >= m.maxSessions {
		m.mu.Unlock()
		return fmt.Errorf("terminal connection limit %d reached", m.maxSessions)
	}
	m.mu.Unlock()

	ctx, cancel := context.WithCancel(parent)
	command, tmuxSession, err := m.terminalCommand(ctx, input, workspace, mode)
	if err != nil {
		cancel()
		return err
	}
	command.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	file, err := pty.StartWithSize(command, &pty.Winsize{Cols: uint16(max(input.GetColumns(), 80)), Rows: uint16(max(input.GetRows(), 24))})
	if err != nil {
		cancel()
		return fmt.Errorf("start terminal: %w", err)
	}
	session := &terminalSession{
		id:          input.GetSessionId(),
		boxID:       input.GetBoxId(),
		mode:        mode,
		tmuxSession: tmuxSession,
		command:     command,
		pty:         file,
		cancel:      cancel,
	}
	m.mu.Lock()
	if _, exists := m.sessions[session.id]; exists || len(m.sessions) >= m.maxSessions {
		m.mu.Unlock()
		m.closeSession(session, true)
		return errors.New("terminal connection capacity changed while opening")
	}
	m.sessions[session.id] = session
	m.mu.Unlock()
	go m.copyOutput(session)
	return nil
}

func (m *TerminalManager) terminalCommand(ctx context.Context, input *hostv1.TerminalInput, workspace, mode string) (*exec.Cmd, string, error) {
	if mode == terminalModeCommand {
		shell := "/bin/sh"
		args := []string{"-l"}
		if runtime.GOOS == "windows" {
			shell = "powershell.exe"
			args = nil
		} else if _, err := os.Stat("/bin/zsh"); err == nil {
			shell = "/bin/zsh"
		}
		command := exec.CommandContext(ctx, shell, args...)
		command.Dir = workspace
		return command, "", nil
	}
	if strings.TrimSpace(input.GetBoxId()) == "" || strings.TrimSpace(input.GetRuntimeType()) == "" {
		return nil, "", errors.New("agent terminal requires boxId and runtimeType")
	}
	tmuxSession := tmuxSessionName(input.GetBoxId())
	if err := m.ensureAgentSession(ctx, tmuxSession, workspace, input.GetRuntimeType(), input.GetModel(), input.GetRuntimeSessionMode(), input.GetRuntimeSessionRef()); err != nil {
		return nil, "", err
	}
	return exec.CommandContext(ctx, m.tmuxBinary, "attach-session", "-t", "="+tmuxSession), tmuxSession, nil
}

func (m *TerminalManager) ensureAgentSession(ctx context.Context, sessionName, workspace, runtimeType, model, sessionMode, sessionRef string) error {
	if runtimeType == "claude" {
		if err := m.ensureTmuxUpdateEnvironment(ctx, "ANTHROPIC_API_KEY"); err != nil {
			return err
		}
	}
	if m.tmuxSessionExists(ctx, sessionName) {
		return nil
	}
	runtimeCommand, err := agentRuntimeCommand(runtimeType, model, sessionMode, sessionRef)
	if err != nil {
		return err
	}
	args := []string{"new-session", "-d", "-s", sessionName, "-c", workspace, "--"}
	args = append(args, runtimeCommand...)
	output, err := exec.CommandContext(ctx, m.tmuxBinary, args...).CombinedOutput()
	if err != nil {
		if m.tmuxSessionExists(ctx, sessionName) {
			return nil
		}
		return fmt.Errorf("create tmux Agent Terminal: %w: %s", err, boundedTerminalOutput(output))
	}
	_ = exec.CommandContext(ctx, m.tmuxBinary, "set-option", "-t", "="+sessionName, "history-limit", "50000").Run()
	_ = exec.CommandContext(ctx, m.tmuxBinary, "set-option", "-t", "="+sessionName, "mouse", "on").Run()
	return nil
}

func agentRuntimeCommand(runtimeType, model, sessionMode, sessionRef string) ([]string, error) {
	sessionMode = strings.ToLower(strings.TrimSpace(sessionMode))
	sessionRef = strings.TrimSpace(sessionRef)
	if sessionMode == "attach" {
		if runtimeType != "claude" || sessionRef == "" {
			return nil, errors.New("claude attach requires a session reference")
		}
		return []string{"claude", "attach", sessionRef}, nil
	}
	var command []string
	switch runtimeType {
	case "omp":
		command = []string{"omp", "--approval-mode", "yolo"}
	case "codex":
		command = []string{"codex", "--dangerously-bypass-approvals-and-sandbox"}
	case "claude":
		command = []string{"claude", "--permission-mode", "bypassPermissions", "--allow-dangerously-skip-permissions"}
	default:
		return nil, fmt.Errorf("runtime %q does not support native Agent Terminal", runtimeType)
	}
	if sessionMode == "resume" {
		if runtimeType != "claude" || sessionRef == "" {
			return nil, errors.New("claude resume requires a session reference")
		}
		command = append(command, "--resume", sessionRef)
	}
	if model = strings.TrimSpace(model); model != "" {
		command = append(command, "--model", model)
	}
	return command, nil
}

func (m *TerminalManager) ensureTmuxUpdateEnvironment(ctx context.Context, names ...string) error {
	output, err := exec.CommandContext(ctx, m.tmuxBinary, "show-options", "-gv", "update-environment").Output()
	if err != nil {
		// With no tmux server, the first new-session inherits the daemon environment directly.
		return nil
	}
	updated := strings.Fields(string(output))
	changed := false
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || containsString(updated, name) {
			continue
		}
		updated = append(updated, name)
		changed = true
	}
	if !changed {
		return nil
	}
	if output, err := exec.CommandContext(ctx, m.tmuxBinary, "set-option", "-g", "update-environment", strings.Join(updated, " ")).CombinedOutput(); err != nil {
		return fmt.Errorf("configure tmux environment: %w: %s", err, boundedTerminalOutput(output))
	}
	return nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (m *TerminalManager) tmuxSessionExists(ctx context.Context, sessionName string) bool {
	return exec.CommandContext(ctx, m.tmuxBinary, "has-session", "-t", "="+sessionName).Run() == nil
}

func (m *TerminalManager) terminateAgentSession(ctx context.Context, boxID string) error {
	sessionName := tmuxSessionName(boxID)
	m.mu.Lock()
	active := make([]*terminalSession, 0)
	for _, session := range m.sessions {
		if session.mode == terminalModeAgent && session.boxID == boxID {
			active = append(active, session)
		}
	}
	m.mu.Unlock()
	for _, session := range active {
		m.closeSession(session, true)
	}
	if !m.tmuxSessionExists(ctx, sessionName) {
		return nil
	}
	output, err := exec.CommandContext(ctx, m.tmuxBinary, "kill-session", "-t", "="+sessionName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("terminate tmux Agent Terminal: %w: %s", err, boundedTerminalOutput(output))
	}
	return nil
}

func (m *TerminalManager) copyOutput(session *terminalSession) {
	buffer := make([]byte, 32<<10)
	for {
		read, err := session.pty.Read(buffer)
		if read > 0 {
			data := append([]byte(nil), buffer[:read]...)
			if emitErr := m.emit(&hostv1.TerminalData{SessionId: session.id, Data: data, Stream: hostv1.TerminalStream_TERMINAL_STREAM_STDOUT}); emitErr != nil {
				m.closeSession(session, true)
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) {
				_ = m.emit(&hostv1.TerminalData{SessionId: session.id, Stream: hostv1.TerminalStream_TERMINAL_STREAM_STDERR, Error: err.Error()})
			}
			break
		}
	}
	waitErr := session.command.Wait()
	exitCode := int32(0)
	errorText := ""
	if waitErr != nil {
		exitCode = int32(session.command.ProcessState.ExitCode())
		errorText = waitErr.Error()
	}
	_ = m.emit(&hostv1.TerminalData{SessionId: session.id, Closed: true, ExitCode: exitCode, Error: errorText})
	m.closeSession(session, false)
}

func (m *TerminalManager) closeSession(session *terminalSession, terminate bool) {
	session.once.Do(func() {
		m.mu.Lock()
		delete(m.sessions, session.id)
		m.mu.Unlock()
		if terminate && session.command.Process != nil {
			_ = session.command.Process.Kill()
		}
		session.cancel()
		_ = session.pty.Close()
	})
}

func (m *TerminalManager) Close() {
	m.mu.Lock()
	sessions := make([]*terminalSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.Unlock()
	for _, session := range sessions {
		m.closeSession(session, true)
	}
}

func tmuxSessionName(boxID string) string {
	var builder strings.Builder
	builder.WriteString("abox-agent-")
	for _, character := range boxID {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '-' || character == '_' {
			builder.WriteRune(character)
		}
	}
	return builder.String()
}

func boundedTerminalOutput(output []byte) string {
	const limit = 2048
	text := strings.TrimSpace(string(output))
	if len(text) > limit {
		return text[len(text)-limit:]
	}
	return text
}

func max(value, fallback uint32) uint32 {
	if value > fallback {
		return value
	}
	return fallback
}
