package mcpserver

import (
	"encoding/json"
	"errors"

	"lore/internal/core"
	"lore/internal/idempotency"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type externalError struct {
	SchemaVersion int    `json:"schema_version"`
	Status        string `json:"status"`
	RequestID     string `json:"request_id"`
	Code          string `json:"code"`
	Message       string `json:"message"`
	ErrorID       string `json:"error_id,omitempty"`
}

type toolError struct {
	code    string
	message string
}

func (e *toolError) Error() string {
	return e.message
}

func mappedToolError(err error, requestID string) (*mcp.CallToolResult, error) {
	code, message := "internal_error", "The Lore operation failed unexpectedly."
	errorID := ""
	var apiErr *core.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "reference_not_found":
			code, message = "not_found", "The requested document was not found."
		case "permission_denied":
			code, message = "permission_denied", "The principal is not authorized for this operation."
		case "ambiguous_reference":
			code, message = "conflict", "The document reference is ambiguous."
		case "repository_locked":
			code, message = "locked", "The Lore repository is busy."
		case "prospective_lint_invalid", "lint_invalid", "prospective_lint_changed":
			code, message = "lint_failed", "The Lore operation did not pass canonical lint."
		case "recovery_required":
			code, message = "recovery_required", "Repository recovery is required before this write."
		case "git_push_failed", "push_required_failed":
			code, message = "required_push_failed", "The canonical change is locally safe, but the required Git push failed."
		case "transaction_integrity_failed", "commit_content_mismatch":
			code, message = "integrity_error", "Stored Lore operation data failed integrity verification."
		default:
			switch apiErr.ExitCode {
			case core.ExitUsage, core.ExitValidation:
				code, message = "invalid_argument", "The tool arguments are invalid."
			case core.ExitConflict:
				code, message = "conflict", "The Lore operation conflicts with current repository state."
			}
		}
	}
	if code == "internal_error" {
		errorID = newID("err")
	}
	envelope := externalError{
		SchemaVersion: schemaVersion,
		Status:        "error",
		RequestID:     requestID,
		Code:          code,
		Message:       message,
		ErrorID:       errorID,
	}
	data, marshalErr := json.Marshal(envelope)
	if marshalErr != nil {
		data = []byte(`{"schema_version":1,"status":"error","code":"internal_error","message":"The Lore operation failed unexpectedly."}`)
	}
	result := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}
	return result, &toolError{code: code, message: string(data)}
}

func mappedIdempotencyError(err error, requestID string) (*mcp.CallToolResult, error) {
	var conflict *idempotency.ConflictError
	if errors.As(err, &conflict) {
		return mappedToolError(core.NewError(core.ExitConflict, "idempotency_conflict", "idempotency key input conflicts with its existing operation"), requestID)
	}
	var locked *idempotency.LockedError
	if errors.As(err, &locked) {
		return mappedToolError(core.NewError(core.ExitConflict, "repository_locked", "idempotent operation is already running"), requestID)
	}
	return mappedToolError(err, requestID)
}
