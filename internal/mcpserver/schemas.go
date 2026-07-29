package mcpserver

import (
	"time"

	"lore/internal/catalog"
	"lore/internal/core"
	"lore/internal/gitx"
	loreindex "lore/internal/index"
	"lore/internal/lint"
	"lore/internal/search"
	"lore/internal/transaction"
)

const (
	schemaVersion      = 1
	maximumSearchLimit = 50
	maximumRecentLimit = 100
	defaultReadLines   = 160
	maximumReadLines   = 400
	maximumReadBytes   = 256 * 1024
	maximumDiffBytes   = 256 * 1024
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
	SchemaVersion                int            `json:"schema_version"`
	Status                       string         `json:"status"`
	RequestID                    string         `json:"request_id"`
	Operation                    string         `json:"operation"`
	Valid                        bool           `json:"valid"`
	Errors                       int            `json:"errors"`
	Warnings                     int            `json:"warnings"`
	Findings                     []lint.Finding `json:"findings"`
	AdditionalInaccessibleErrors bool           `json:"additional_inaccessible_errors"`
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
	CountsDisclosed    bool                `json:"counts_disclosed"`
	DocumentCount      *int                `json:"document_count,omitempty"`
	PageCount          *int                `json:"page_count,omitempty"`
	SourceCount        *int                `json:"source_count,omitempty"`
	IndexedHead        string              `json:"indexed_head"`
	CurrentHead        string              `json:"current_head"`
	HeadMatches        bool                `json:"head_matches"`
	Verification       string              `json:"verification"`
	Warnings           []loreindex.Warning `json:"warnings"`
}

type CaptureInput struct {
	Kind           string   `json:"kind"`
	Origin         string   `json:"origin"`
	Text           string   `json:"text"`
	Sensitivity    string   `json:"sensitivity,omitempty"`
	OriginRef      string   `json:"origin_ref,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	IdempotencyKey string   `json:"idempotency_key,omitempty"`
}

type CaptureOutput struct {
	SchemaVersion int      `json:"schema_version"`
	Status        string   `json:"status"`
	RequestID     string   `json:"request_id"`
	Operation     string   `json:"operation"`
	ID            string   `json:"id"`
	Path          string   `json:"path"`
	URI           string   `json:"uri"`
	CapturedAt    string   `json:"captured_at"`
	RawSHA256     string   `json:"raw_sha256"`
	Revision      string   `json:"revision"`
	Bytes         int      `json:"bytes"`
	Written       bool     `json:"written"`
	Committed     bool     `json:"committed"`
	Commit        string   `json:"commit"`
	Pushed        bool     `json:"pushed"`
	Replayed      bool     `json:"replayed"`
	Warnings      []string `json:"warnings"`
}

type PreviewInput struct {
	SchemaVersion int                     `json:"schema_version"`
	Message       string                  `json:"message"`
	Operations    []transaction.Operation `json:"operations"`
}

type PreviewOutput struct {
	SchemaVersion int         `json:"schema_version"`
	Status        string      `json:"status"`
	RequestID     string      `json:"request_id"`
	Operation     string      `json:"operation"`
	TransactionID string      `json:"transaction_id"`
	CreatedAt     string      `json:"created_at"`
	BaseCommit    string      `json:"base_commit"`
	BaseBranch    string      `json:"base_branch"`
	PreviewDigest string      `json:"preview_digest"`
	ChangedPaths  []string    `json:"changed_paths"`
	Operations    int         `json:"operations"`
	DiffSHA256    string      `json:"diff_sha256"`
	Diff          string      `json:"diff"`
	DiffTruncated bool        `json:"diff_truncated"`
	Lint          lint.Result `json:"lint"`
	Warnings      []string    `json:"warnings"`
}

type CommitInput struct {
	TransactionID  string `json:"transaction_id"`
	PreviewDigest  string `json:"preview_digest"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type CommitOutput struct {
	SchemaVersion    int                `json:"schema_version"`
	Status           string             `json:"status"`
	RequestID        string             `json:"request_id"`
	Operation        string             `json:"operation"`
	TransactionID    string             `json:"transaction_id"`
	TransactionState transaction.Status `json:"transaction_state"`
	PreviewDigest    string             `json:"preview_digest"`
	Commit           string             `json:"commit"`
	ChangedPaths     []string           `json:"changed_paths"`
	CommittedAt      string             `json:"committed_at"`
	Pushed           bool               `json:"pushed"`
	AlreadyCommitted bool               `json:"already_committed"`
	Warnings         []string           `json:"warnings"`
}

type TransactionListInput struct {
	State string `json:"state,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type TransactionListOutput struct {
	SchemaVersion int                       `json:"schema_version"`
	Status        string                    `json:"status"`
	RequestID     string                    `json:"request_id"`
	Operation     string                    `json:"operation"`
	Transactions  []core.TransactionSummary `json:"transactions"`
}

type TransactionShowInput struct {
	TransactionID string `json:"transaction_id"`
}

type TransactionShowOutput struct {
	SchemaVersion int                  `json:"schema_version"`
	Status        string               `json:"status"`
	RequestID     string               `json:"request_id"`
	Operation     string               `json:"operation"`
	Proposal      transaction.Proposal `json:"proposal"`
	State         transaction.State    `json:"state"`
	PreviewDigest string               `json:"preview_digest"`
	Lint          lint.Result          `json:"lint"`
	Diff          string               `json:"diff"`
	DiffTruncated bool                 `json:"diff_truncated"`
}

type TransactionDiscardInput struct {
	TransactionID string `json:"transaction_id"`
}

type TransactionDiscardOutput struct {
	SchemaVersion    int                `json:"schema_version"`
	Status           string             `json:"status"`
	RequestID        string             `json:"request_id"`
	Operation        string             `json:"operation"`
	TransactionID    string             `json:"transaction_id"`
	TransactionState transaction.Status `json:"transaction_state"`
	Discarded        bool               `json:"discarded"`
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
