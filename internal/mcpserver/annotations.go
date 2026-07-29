package mcpserver

import "github.com/modelcontextprotocol/go-sdk/mcp"

func readOnlyAnnotations(title string) *mcp.ToolAnnotations {
	no := false
	return &mcp.ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    true,
		IdempotentHint:  true,
		DestructiveHint: &no,
		OpenWorldHint:   &no,
	}
}
