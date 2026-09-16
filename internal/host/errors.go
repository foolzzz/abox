package host

import (
	"errors"
	"fmt"
)

type ErrorCode string

const (
	CodeConfiguration  ErrorCode = "configuration"
	CodeAuthentication ErrorCode = "authentication"
	CodeProtocol       ErrorCode = "protocol"
	CodePersistence    ErrorCode = "persistence"
	CodeJournalFull    ErrorCode = "journal_full"
	CodeJournalCorrupt ErrorCode = "journal_corrupt"
	CodeIdempotency    ErrorCode = "idempotency"
	CodeTransport      ErrorCode = "transport"
	CodeCommand        ErrorCode = "command"
)

var (
	ErrJournalFull    = errors.New("host journal is full")
	ErrJournalCorrupt = errors.New("host journal is corrupt")
	ErrStoreFull      = errors.New("host idempotency store is full")
)

type ProtocolError struct {
	Op   string
	Code ErrorCode
	Err  error
}

func (e *ProtocolError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Op == "" {
		return fmt.Sprintf("host protocol %s: %v", e.Code, e.Err)
	}
	return fmt.Sprintf("host protocol %s (%s): %v", e.Op, e.Code, e.Err)
}

func (e *ProtocolError) Unwrap() error { return e.Err }

func protocolError(op string, code ErrorCode, err error) error {
	if err == nil {
		return nil
	}
	return &ProtocolError{Op: op, Code: code, Err: err}
}
