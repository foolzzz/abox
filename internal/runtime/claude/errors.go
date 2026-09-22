package claude

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidHandle                  = errors.New("claude: invalid handle")
	ErrProcessExited                  = errors.New("claude: process exited")
	ErrTurnActive                     = errors.New("claude: a turn is already active")
	ErrTurnQueued                     = errors.New("claude: a follow-up turn is already queued")
	ErrUnsupportedInput               = errors.New("claude: unsupported input kind")
	ErrInteractiveApprovalUnsupported = errors.New("claude: interactive approvals are unsupported")
	ErrAuthenticationRequired         = errors.New("claude: authentication is required")
)

type ProtocolError struct {
	Line   uint64
	Kind   string
	Detail string
	Cause  error
}

func (e *ProtocolError) Error() string {
	location := "claude stream"
	if e.Line != 0 {
		location = fmt.Sprintf("claude stream line %d", e.Line)
	}
	if e.Detail == "" {
		return fmt.Sprintf("%s: %s", location, e.Kind)
	}
	return fmt.Sprintf("%s: %s: %s", location, e.Kind, e.Detail)
}

func (e *ProtocolError) Unwrap() error {
	return e.Cause
}

type OperationError struct {
	Operation string
	Cause     error
	Detail    string
}

func (e *OperationError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("claude %s: %v", e.Operation, e.Cause)
	}
	return fmt.Sprintf("claude %s: %s: %v", e.Operation, e.Detail, e.Cause)
}

func (e *OperationError) Unwrap() error {
	return e.Cause
}
