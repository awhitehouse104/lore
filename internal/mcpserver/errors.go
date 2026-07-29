package mcpserver

import (
	"encoding/json"
	"errors"
	"fmt"

	"lore/internal/core"

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
		case "ambiguous_reference":
			code, message = "conflict", "The document reference is ambiguous."
		case "repository_locked":
			code, message = "locked", "The Lore repository is busy."
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
	return result, &toolError{code: code, message: fmt.Sprintf("%s: %s", code, message)}
}
