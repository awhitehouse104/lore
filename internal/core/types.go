package core

import "fmt"

const SchemaVersion = 1

const (
	ExitOK         = 0
	ExitValidation = 1
	ExitUsage      = 2
	ExitRuntime    = 3
	ExitConflict   = 4
)

// APIError is the stable, adapter-independent error returned by Lore operations.
type APIError struct {
	Code     string         `json:"code"`
	Message  string         `json:"message"`
	Details  map[string]any `json:"details"`
	ExitCode int            `json:"-"`
	Cause    error          `json:"-"`
}

func (e *APIError) Error() string {
	if e.Cause == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.Cause)
}

func (e *APIError) Unwrap() error {
	return e.Cause
}

func NewError(exitCode int, code, message string) *APIError {
	return &APIError{
		Code:     code,
		Message:  message,
		Details:  map[string]any{},
		ExitCode: exitCode,
	}
}

type ErrorEnvelope struct {
	SchemaVersion int       `json:"schema_version"`
	Error         *APIError `json:"error"`
}
