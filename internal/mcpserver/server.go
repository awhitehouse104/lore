package mcpserver

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"time"

	"lore/internal/core"
	loreindex "lore/internal/index"
	"lore/internal/search"
	"lore/internal/version"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oklog/ulid/v2"
)

type Server struct {
	service *core.Service
	mcp     *mcp.Server
}

func New(service *core.Service, logger *slog.Logger) *Server {
	if service == nil {
		panic("nil Lore service")
	}
	server := &Server{service: service}
	server.mcp = mcp.NewServer(
		&mcp.Implementation{Name: "lore", Version: version.Version},
		&mcp.ServerOptions{
			Instructions: "Query and inspect the configured Lore Markdown knowledge repository through typed operations.",
			Logger:       logger,
			PageSize:     100,
			Capabilities: &mcp.ServerCapabilities{},
		},
	)
	server.addTools()
	return server
}

func (s *Server) ProtocolServer() *mcp.Server {
	return s.mcp
}

func (s *Server) Run(ctx context.Context, transport mcp.Transport) error {
	return s.mcp.Run(ctx, transport)
}

func (s *Server) addTools() {
	mcp.AddTool(s.mcp, searchTool(), s.search)
	mcp.AddTool(s.mcp, readTool(), s.read)
	mcp.AddTool(s.mcp, recentTool(), s.recent)
	mcp.AddTool(s.mcp, lintTool(), s.lint)
	mcp.AddTool(s.mcp, indexStatusTool(), s.indexStatus)
}

func (s *Server) search(ctx context.Context, _ *mcp.CallToolRequest, input SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
	requestID := newID("req")
	limit := input.Limit
	if limit == 0 {
		limit = search.DefaultLimit
	}
	scope, err := searchScope(input.Types)
	if err != nil {
		result, toolErr := mappedToolError(core.NewError(core.ExitValidation, "invalid_search_types", err.Error()), requestID)
		return result, SearchOutput{}, toolErr
	}
	backend := search.Backend(input.Backend)
	if backend == "" {
		backend = search.BackendAuto
	}
	result, err := s.service.Search(ctx, search.Query{
		Text:    input.Query,
		Scope:   scope,
		Tags:    input.Tags,
		Paths:   input.Paths,
		Limit:   limit,
		Backend: backend,
		Access:  search.AllAccessPolicy(),
	})
	if err != nil {
		callResult, toolErr := mappedToolError(err, requestID)
		return callResult, SearchOutput{}, toolErr
	}
	output := SearchOutput{
		SchemaVersion:    schemaVersion,
		Status:           "ok",
		RequestID:        requestID,
		Operation:        "lore_search",
		Query:            result.Query,
		Backend:          result.Backend,
		BackendRequested: result.BackendRequested,
		IndexState:       result.IndexState,
		Results:          result.Results,
		Warnings:         result.Warnings,
	}
	summary := fmt.Sprintf("Found %d authorized Lore document(s) using the %s backend.", len(output.Results), output.Backend)
	return textResult(summary), output, nil
}

func (s *Server) read(ctx context.Context, _ *mcp.CallToolRequest, input ReadInput) (*mcp.CallToolResult, ReadOutput, error) {
	requestID := newID("req")
	start := input.StartLine
	if start == 0 {
		start = 1
	}
	end := input.EndLine
	if end == 0 {
		end = start + defaultReadLines - 1
	}
	if start < 1 || end < start || end-start+1 > maximumReadLines {
		err := core.NewError(core.ExitValidation, "invalid_line_range", fmt.Sprintf("read range must contain between 1 and %d lines", maximumReadLines))
		callResult, toolErr := mappedToolError(err, requestID)
		return callResult, ReadOutput{}, toolErr
	}
	result, err := s.service.Read(ctx, input.Ref, &core.LineRange{Start: start, End: end})
	if err != nil {
		callResult, toolErr := mappedToolError(err, requestID)
		return callResult, ReadOutput{}, toolErr
	}
	if len(result.Content) > maximumReadBytes {
		err := core.NewError(core.ExitValidation, "read_too_large", fmt.Sprintf("read result exceeds the %d-byte response limit; request a smaller line range", maximumReadBytes))
		callResult, toolErr := mappedToolError(err, requestID)
		return callResult, ReadOutput{}, toolErr
	}
	output := ReadOutput{
		SchemaVersion: schemaVersion,
		Status:        "ok",
		RequestID:     requestID,
		Operation:     "lore_read",
		Path:          result.Path,
		URI:           result.URI,
		ID:            result.ID,
		Title:         result.Title,
		Kind:          result.Kind,
		Sensitivity:   result.Sensitivity,
		LineStart:     result.LineStart,
		LineEnd:       result.LineEnd,
		More:          result.More,
		Revision:      result.Revision,
		Content:       result.Content,
	}
	summary := fmt.Sprintf("Read %s lines %d-%d (%s).", output.ID, output.LineStart, output.LineEnd, output.Revision)
	return textResult(summary), output, nil
}

func (s *Server) recent(ctx context.Context, _ *mcp.CallToolRequest, input RecentInput) (*mcp.CallToolResult, RecentOutput, error) {
	requestID := newID("req")
	limit := input.Limit
	if limit == 0 {
		limit = core.DefaultRecentLimit
	}
	since, err := parseSince(input.Since)
	if err != nil {
		apiErr := core.NewError(core.ExitValidation, "invalid_since", "since must be an RFC 3339 timestamp")
		callResult, toolErr := mappedToolError(apiErr, requestID)
		return callResult, RecentOutput{}, toolErr
	}
	result, err := s.service.Recent(ctx, core.RecentOptions{Limit: limit, Since: since})
	if err != nil {
		callResult, toolErr := mappedToolError(err, requestID)
		return callResult, RecentOutput{}, toolErr
	}
	output := RecentOutput{
		SchemaVersion: schemaVersion,
		Status:        "ok",
		RequestID:     requestID,
		Operation:     "lore_recent",
		Since:         input.Since,
		Commits:       result.Commits,
	}
	return textResult(fmt.Sprintf("Found %d recent Lore commit(s).", len(output.Commits))), output, nil
}

func (s *Server) lint(ctx context.Context, _ *mcp.CallToolRequest, _ LintInput) (*mcp.CallToolResult, LintOutput, error) {
	requestID := newID("req")
	result, err := s.service.Lint(ctx)
	if err != nil {
		callResult, toolErr := mappedToolError(err, requestID)
		return callResult, LintOutput{}, toolErr
	}
	output := LintOutput{
		SchemaVersion: schemaVersion,
		Status:        "ok",
		RequestID:     requestID,
		Operation:     "lore_lint",
		Valid:         result.Valid,
		Errors:        result.Errors,
		Warnings:      result.Warnings,
		Findings:      result.Findings,
	}
	return textResult(fmt.Sprintf("Lore lint found %d error(s) and %d warning(s).", output.Errors, output.Warnings)), output, nil
}

func (s *Server) indexStatus(ctx context.Context, _ *mcp.CallToolRequest, _ IndexStatusInput) (*mcp.CallToolResult, IndexStatusOutput, error) {
	requestID := newID("req")
	result, err := s.service.IndexStatus(ctx, false)
	if err != nil {
		callResult, toolErr := mappedToolError(err, requestID)
		return callResult, IndexStatusOutput{}, toolErr
	}
	output := IndexStatusOutput{
		SchemaVersion:      schemaVersion,
		Status:             "ok",
		RequestID:          requestID,
		Operation:          "lore_index_status",
		IndexState:         result.IndexState,
		SchemaCompatible:   result.IndexSchemaVersion == 0 || result.IndexSchemaVersion == loreindex.SchemaVersion,
		IndexSchemaVersion: result.IndexSchemaVersion,
		DocumentCount:      result.DocumentCount,
		PageCount:          result.PageCount,
		SourceCount:        result.SourceCount,
		IndexedHead:        result.IndexedHead,
		CurrentHead:        result.CurrentHead,
		HeadMatches:        result.IndexedHead != "" && result.IndexedHead == result.CurrentHead,
		Verification:       result.Verification,
		Warnings:           result.Warnings,
	}
	return textResult(fmt.Sprintf("Lore index state is %s with %d indexed document(s).", output.IndexState, output.DocumentCount)), output, nil
}

func searchScope(types []string) (search.Scope, error) {
	if len(types) == 0 {
		return search.ScopeAll, nil
	}
	page, source := false, false
	for _, documentType := range types {
		switch documentType {
		case "page":
			page = true
		case "source":
			source = true
		default:
			return "", fmt.Errorf("search types may contain only page or source")
		}
	}
	switch {
	case page && source:
		return search.ScopeAll, nil
	case page:
		return search.ScopePages, nil
	case source:
		return search.ScopeSources, nil
	default:
		return "", fmt.Errorf("search types must not be empty")
	}
}

func textResult(summary string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: summary}}}
}

func newID(prefix string) string {
	value, err := ulid.New(ulid.Timestamp(time.Now().UTC()), rand.Reader)
	if err != nil {
		return prefix + "_unavailable"
	}
	return prefix + "_" + value.String()
}

func searchTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "lore_search",
		Title:       "Search Lore",
		Description: "Search authorized Lore pages and captured sources without returning full document bodies.",
		Annotations: readOnlyAnnotations("Search Lore"),
		InputSchema: objectSchema(
			[]string{"query"},
			map[string]any{
				"query":   map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
				"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": maximumSearchLimit, "default": search.DefaultLimit},
				"types":   map[string]any{"type": "array", "maxItems": 2, "uniqueItems": true, "items": map[string]any{"type": "string", "enum": []string{"page", "source"}}},
				"tags":    stringArraySchema(20, 128),
				"paths":   stringArraySchema(20, 1024),
				"backend": map[string]any{"type": "string", "enum": []string{"auto", "index", "filesystem"}, "default": "auto"},
			},
		),
	}
}

func readTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "lore_read",
		Title:       "Read Lore document",
		Description: "Read a bounded line range from one managed Lore document by ID, Lore URI, or exact managed path.",
		Annotations: readOnlyAnnotations("Read Lore document"),
		InputSchema: objectSchema(
			[]string{"ref"},
			map[string]any{
				"ref":        map[string]any{"type": "string", "minLength": 1, "maxLength": 2048},
				"start_line": map[string]any{"type": "integer", "minimum": 1},
				"end_line":   map[string]any{"type": "integer", "minimum": 1},
			},
		),
	}
}

func recentTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "lore_recent",
		Title:       "Recent Lore history",
		Description: "List bounded recent Git commit metadata for managed Lore knowledge without raw diffs.",
		Annotations: readOnlyAnnotations("Recent Lore history"),
		InputSchema: objectSchema(
			nil,
			map[string]any{
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": maximumRecentLimit, "default": core.DefaultRecentLimit},
				"since": map[string]any{"type": "string", "format": "date-time", "maxLength": 64},
			},
		),
	}
}

func lintTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "lore_lint",
		Title:       "Lint Lore",
		Description: "Run deterministic canonical validation for the configured Lore repository.",
		Annotations: readOnlyAnnotations("Lint Lore"),
		InputSchema: objectSchema(nil, map[string]any{}),
	}
}

func indexStatusTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "lore_index_status",
		Title:       "Inspect Lore index",
		Description: "Inspect derived search-index health, compatibility, freshness, counts, and warning codes.",
		Annotations: readOnlyAnnotations("Inspect Lore index"),
		InputSchema: objectSchema(nil, map[string]any{}),
	}
}

func objectSchema(required []string, properties map[string]any) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringArraySchema(maxItems, maxLength int) map[string]any {
	return map[string]any{
		"type":     "array",
		"maxItems": maxItems,
		"items":    map[string]any{"type": "string", "minLength": 1, "maxLength": maxLength},
	}
}
