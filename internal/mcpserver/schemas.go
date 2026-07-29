package mcpserver

import (
	"time"

	"lore/internal/catalog"
	"lore/internal/gitx"
	loreindex "lore/internal/index"
	"lore/internal/lint"
	"lore/internal/search"
)

const (
	schemaVersion      = 1
	maximumSearchLimit = 50
	maximumRecentLimit = 100
	defaultReadLines   = 160
	maximumReadLines   = 400
	maximumReadBytes   = 256 * 1024
)

type SearchInput struct {
	Query   string   `json:"query"`
	Limit   int      `json:"limit,omitempty"`
	Types   []string `json:"types,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Paths   []string `json:"paths,omitempty"`
	Backend string   `json:"backend,omitempty"`
}

type SearchOutput struct {
	SchemaVersion    int               `json:"schema_version"`
	Status           string            `json:"status"`
	RequestID        string            `json:"request_id"`
	Operation        string            `json:"operation"`
	Query            string            `json:"query"`
	Backend          search.Backend    `json:"backend"`
	BackendRequested search.Backend    `json:"backend_requested"`
	IndexState       string            `json:"index_state"`
	Results          []search.Result   `json:"results"`
	Warnings         []catalog.Warning `json:"warnings"`
}

type ReadInput struct {
	Ref       string `json:"ref"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
}

type ReadOutput struct {
	SchemaVersion int    `json:"schema_version"`
	Status        string `json:"status"`
	RequestID     string `json:"request_id"`
	Operation     string `json:"operation"`
	Path          string `json:"path"`
	URI           string `json:"uri"`
	ID            string `json:"id"`
	Title         string `json:"title"`
	Kind          string `json:"kind"`
	Sensitivity   string `json:"sensitivity"`
	LineStart     int    `json:"line_start"`
	LineEnd       int    `json:"line_end"`
	More          bool   `json:"more"`
	Revision      string `json:"revision"`
	Content       string `json:"content"`
}

type RecentInput struct {
	Limit int    `json:"limit,omitempty"`
	Since string `json:"since,omitempty"`
}

type RecentOutput struct {
	SchemaVersion int           `json:"schema_version"`
	Status        string        `json:"status"`
	RequestID     string        `json:"request_id"`
	Operation     string        `json:"operation"`
	Since         string        `json:"since,omitempty"`
	Commits       []gitx.Commit `json:"commits"`
}

type LintInput struct{}

type LintOutput struct {
	SchemaVersion int            `json:"schema_version"`
	Status        string         `json:"status"`
	RequestID     string         `json:"request_id"`
	Operation     string         `json:"operation"`
	Valid         bool           `json:"valid"`
	Errors        int            `json:"errors"`
	Warnings      int            `json:"warnings"`
	Findings      []lint.Finding `json:"findings"`
}

type IndexStatusInput struct{}

type IndexStatusOutput struct {
	SchemaVersion      int                 `json:"schema_version"`
	Status             string              `json:"status"`
	RequestID          string              `json:"request_id"`
	Operation          string              `json:"operation"`
	IndexState         loreindex.State     `json:"index_state"`
	SchemaCompatible   bool                `json:"schema_compatible"`
	IndexSchemaVersion int                 `json:"index_schema_version"`
	DocumentCount      int                 `json:"document_count"`
	PageCount          int                 `json:"page_count"`
	SourceCount        int                 `json:"source_count"`
	IndexedHead        string              `json:"indexed_head"`
	CurrentHead        string              `json:"current_head"`
	HeadMatches        bool                `json:"head_matches"`
	Verification       string              `json:"verification"`
	Warnings           []loreindex.Warning `json:"warnings"`
}

func parseSince(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, err
	}
	parsed = parsed.UTC()
	return &parsed, nil
}
