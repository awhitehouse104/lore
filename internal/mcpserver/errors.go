package mcpserver

import (
	"encoding/json"
	"errors"
	"sort"

	"lore/internal/core"
	"lore/internal/docs"
	"lore/internal/idempotency"
	"lore/internal/transaction"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type externalError struct {
	SchemaVersion int            `json:"schema_version"`
	Status        string         `json:"status"`
	RequestID     string         `json:"request_id"`
	Code          string         `json:"code"`
	Reason        string         `json:"reason,omitempty"`
	Message       string         `json:"message"`
	Details       map[string]any `json:"details,omitempty"`
	ErrorID       string         `json:"error_id,omitempty"`
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
	reason := ""
	var details map[string]any
	errorID := ""
	var apiErr *core.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "reference_not_found":
			code, message = "not_found", "The requested document was not found."
		case "transaction_unavailable":
			code, message = "not_found", "No transaction with this ID is available to the current actor. Preview and commit must use the same actor and interface."
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
				reason, message, details = safeValidationDisclosure(apiErr, message)
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
		Reason:        reason,
		Message:       message,
		Details:       details,
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

func safeValidationDisclosure(apiErr *core.APIError, fallbackMessage string) (string, string, map[string]any) {
	if apiErr == nil {
		return "", fallbackMessage, nil
	}
	switch apiErr.Code {
	case "updated_too_old":
		details := make(map[string]any, 3)
		for _, key := range []string{"field", "minimum", "path"} {
			if value, ok := apiErr.Details[key].(string); ok && value != "" {
				details[key] = value
			}
		}
		if len(details) != 3 {
			return "", fallbackMessage, nil
		}
		return "updated_too_old",
			"A content-changing page update must set updated to at least the current UTC calendar date.",
			details
	case "integrated_page_missing":
		pageIDs, ok := apiErr.Details["page_ids"].([]string)
		if !ok || len(pageIDs) == 0 || len(pageIDs) > transaction.MaxIntegrationPages {
			return "", fallbackMessage, nil
		}
		pageIDs = append([]string(nil), pageIDs...)
		for _, pageID := range pageIDs {
			if err := docs.ValidatePageID(pageID); err != nil {
				return "", fallbackMessage, nil
			}
		}
		sort.Strings(pageIDs)
		return "integrated_page_missing",
			"New source integration IDs must name pages present after the transaction.",
			map[string]any{
				"field":    "operations[].page_ids",
				"page_ids": pageIDs,
			}
	default:
		return "", fallbackMessage, nil
	}
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
