package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"

	hostv1 "agentbox/api"
	"github.com/creack/pty"
)

type terminalSession struct {
	id      string
	command *exec.Cmd
	pty     *os.File
	cancel  context.CancelFunc
	once    sync.Once
}

type TerminalManager struct {
	guard       *WorkspaceGuard
	maxSessions int
	emit        func(*hostv1.TerminalData) error

	mu       sync.Mutex
	sessions map[string]*terminalSession
}

func NewTerminalManager(guard *WorkspaceGuard, maxSessions int, emit func(*hostv1.TerminalData) error) (*TerminalManager, error) {
	if guard == nil || emit == nil {
		return nil, errors.New("workspace guard and terminal emitter are required")
	}
	if maxSessions <= 0 {
		return nil, errors.New("terminal session limit must be positive")
	}
	return &TerminalManager{
		guard:       guard,
		maxSessions: maxSessions,
		emit:        emit,
		sessions:    make(map[string]*terminalSession),
	}, nil
}

func (m *TerminalManager) HandleTerminal(ctx context.Context, input *hostv1.TerminalInput) error {
	if input == nil || input.GetSessionId() == "" {
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
	m.mu.Lock()
	if _, exists := m.sessions[input.GetSessionId()]; exists {
		m.mu.Unlock()
		return fmt.Errorf("terminal session %q already exists", input.GetSessionId())
	}
	if len(m.sessions) >= m.maxSessions {
		m.mu.Unlock()
		return fmt.Errorf("terminal session limit %d reached", m.maxSessions)
	}
	ctx, cancel := context.WithCancel(parent)
	shell := "/bin/sh"
	args := []string{"-l"}
	if runtime.GOOS == "windows" {
		shell = "powershell.exe"
		args = nil
	} else if _, statErr := os.Stat("/bin/zsh"); statErr == nil {
		shell = "/bin/zsh"
	}
	command := exec.CommandContext(ctx, shell, args...)
	command.Dir = workspace
	command.Env = append(os.Environ(), "TERM=xterm-256color")
	file, err := pty.StartWithSize(command, &pty.Winsize{Cols: uint16(max(input.GetColumns(), 80)), Rows: uint16(max(input.GetRows(), 24))})
	if err != nil {
		cancel()
		m.mu.Unlock()
		return fmt.Errorf("start terminal: %w", err)
	}
	session := &terminalSession{id: input.GetSessionId(), command: command, pty: file, cancel: cancel}
	m.sessions[session.id] = session
	m.mu.Unlock()
	go m.copyOutput(session)
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

func max(value, fallback uint32) uint32 {
	if value > fallback {
		return value
	}
	return fallback
}
