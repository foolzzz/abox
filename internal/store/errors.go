package store

import "errors"

var (
	ErrNotFound       = errors.New("not found")
	ErrForbidden      = errors.New("forbidden")
	ErrConflict       = errors.New("conflict")
	ErrInvalidState   = errors.New("invalid state")
	ErrHostOffline    = errors.New("host offline")
	ErrRuntimeMissing = errors.New("runtime missing")
)
