package codex

import (
	"errors"
	"fmt"
	"strings"
)

const (
	codeInvalidSpec      = "invalid_spec"
	codeProcessStart     = "process_start"
	codeProcessExit      = "process_exit"
	codeProtocol         = "protocol_error"
	codeFrameTooLarge    = "frame_too_large"
	codeRequestTooLarge  = "request_too_large"
	codeRequestFailed    = "request_failed"
	codeRequestTimeout   = "request_timeout"
	codeUnsupportedInput = "unsupported_input"
	codeInvalidHandle    = "invalid_handle"
	codeRuntimeStopped   = "runtime_stopped"
	codeEventBufferFull  = "event_buffer_full"
	codeAuthentication   = "authentication_required"
)

// Error is a stable, machine-readable failure returned by the Codex adapter.
type Error struct {
	Op     string
	Code   string
	Method string
	Err    error
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
		b.WriteString("codex_error")
	}
	if e.Method != "" {
		fmt.Fprintf(&b, " (method %q)", e.Method)
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
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
	var adapterErr *Error
	if errors.As(err, &adapterErr) {
		return adapterErr.Code
	}
	return ""
}
