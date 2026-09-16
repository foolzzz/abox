package omp

import (
	"errors"
	"fmt"
	"strings"
)

const (
	codeInvalidSpec      = "invalid_spec"
	codeProcessStart     = "process_start"
	codeProcessExit      = "process_exit"
	codeReadyTimeout     = "ready_timeout"
	codeProtocol         = "protocol_error"
	codeFrameTooLarge    = "frame_too_large"
	codeCommandTooLarge  = "command_too_large"
	codeCommandFailed    = "command_failed"
	codeUnsupportedInput = "unsupported_input"
	codeInvalidHandle    = "invalid_handle"
	codeEventBufferFull  = "event_buffer_full"
	codeRuntimeStopped   = "runtime_stopped"
)

// Error is a stable, machine-readable failure returned by the OMP adapter.
type Error struct {
	Op      string
	Code    string
	Command string
	Stderr  string
	Err     error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}

	var b strings.Builder
	if e.Op != "" {
		b.WriteString(e.Op)
		b.WriteString(": ")
	}
	if e.Code != "" {
		b.WriteString(e.Code)
	} else {
		b.WriteString("omp_error")
	}
	if e.Command != "" {
		fmt.Fprintf(&b, " (command %q)", e.Command)
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	if e.Stderr != "" {
		b.WriteString("; stderr: ")
		b.WriteString(e.Stderr)
	}
	return b.String()
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func errorCode(err error) string {
	var rpcErr *Error
	if errors.As(err, &rpcErr) {
		return rpcErr.Code
	}
	return ""
}
