package mcpserver

import (
	"context"
	"time"

	"lore/internal/audit"
	"lore/internal/auth"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type requestIDContextKey struct{}

func auditMiddleware(recorder *audit.Recorder, principal auth.Principal) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (result mcp.Result, returnErr error) {
			operation := auditedOperation(method, request)
			if operation == "" {
				return next(ctx, method, request)
			}
			requestID := newID("req")
			ctx = context.WithValue(ctx, requestIDContextKey{}, requestID)
			started := time.Now()
			outcome := "success"
			errorID := ""
			defer func() {
				if recover() != nil {
					outcome = "panic"
					errorID = newID("err")
					result = nil
					returnErr = &jsonrpc.Error{
						Code:    jsonrpc.CodeInternalError,
						Message: "Internal error",
						Data:    []byte(`{"error_id":"` + errorID + `"}`),
					}
				} else if returnErr != nil || resultIsError(result) {
					outcome = "error"
				}
				recorder.Record(audit.Event{
					RequestID: requestID,
					Transport: string(principal.Transport),
					Principal: principal.ID,
					Operation: operation,
					Outcome:   outcome,
					Duration:  time.Since(started),
					ErrorID:   errorID,
				})
			}()
			return next(ctx, method, request)
		}
	}
}

func requestID(ctx context.Context) string {
	if requestID, ok := ctx.Value(requestIDContextKey{}).(string); ok && requestID != "" {
		return requestID
	}
	return newID("req")
}

func auditedOperation(method string, request mcp.Request) string {
	switch method {
	case "tools/call":
		if call, ok := request.(*mcp.CallToolRequest); ok && call.Params != nil {
			return call.Params.Name
		}
		return "tools/call"
	case "server/discover", "tools/list", "resources/list", "resources/templates/list", "resources/read":
		return method
	default:
		return ""
	}
}

func resultIsError(result mcp.Result) bool {
	if toolResult, ok := result.(*mcp.CallToolResult); ok {
		return toolResult.IsError
	}
	return false
}
