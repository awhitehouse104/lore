package index

import (
	"fmt"
	"time"

	"lore/internal/repository"
)

const (
	SchemaVersion     = 1
	RelativeIndexPath = ".lore/index.sqlite"
)

type State string

const (
	StateMissing      State = "missing"
	StateBuilding     State = "building"
	StateFresh        State = "fresh"
	StateStale        State = "stale"
	StateCorrupt      State = "corrupt"
	StateIncompatible State = "incompatible"
	StateUncertified  State = "uncertified"
)

type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Status struct {
	SchemaVersion             int       `json:"schema_version"`
	Status                    string    `json:"status"`
	IndexState                State     `json:"index_state"`
	Path                      string    `json:"path"`
	IndexSchemaVersion        int       `json:"index_schema_version"`
	DocumentCount             int       `json:"document_count"`
	PageCount                 int       `json:"page_count"`
	SourceCount               int       `json:"source_count"`
	IndexedHead               string    `json:"indexed_head"`
	CurrentHead               string    `json:"current_head"`
	IndexedBranch             string    `json:"indexed_branch"`
	CurrentBranch             string    `json:"current_branch"`
	IndexedAt                 string    `json:"indexed_at"`
	IndexBuildID              string    `json:"index_build_id"`
	SQLiteVersion             string    `json:"sqlite_version"`
	RepositoryIdentityMatches bool      `json:"repository_identity_matches"`
	ManagedWorktreeClean      bool      `json:"managed_worktree_clean"`
	ManifestMatches           bool      `json:"manifest_matches"`
	Verification              string    `json:"verification"`
	SecureDelete              bool      `json:"secure_delete"`
	FTS5SecureDelete          bool      `json:"fts5_secure_delete"`
	Warnings                  []Warning `json:"warnings"`
}

type BuildResult struct {
	SchemaVersion int       `json:"schema_version"`
	Status        string    `json:"status"`
	IndexState    State     `json:"index_state"`
	Path          string    `json:"path"`
	DocumentCount int       `json:"document_count"`
	PageCount     int       `json:"page_count"`
	SourceCount   int       `json:"source_count"`
	IndexedHead   string    `json:"indexed_head"`
	IndexedBranch string    `json:"indexed_branch"`
	IndexedAt     string    `json:"indexed_at"`
	IndexBuildID  string    `json:"index_build_id"`
	DurationMS    int64     `json:"duration_ms"`
	Warnings      []Warning `json:"warnings"`
}

type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time {
	return time.Now()
}

type Manager struct {
	Repo        *repository.Repository
	Git         Git
	Clock       Clock
	LoreVersion string
}

func NewManager(repo *repository.Repository, git Git, loreVersion string) *Manager {
	return &Manager{
		Repo:        repo,
		Git:         git,
		Clock:       realClock{},
		LoreVersion: loreVersion,
	}
}

type ErrorClass string

const (
	ErrorValidation ErrorClass = "validation"
	ErrorUsage      ErrorClass = "usage"
	ErrorRuntime    ErrorClass = "runtime"
	ErrorConflict   ErrorClass = "conflict"
)

type Error struct {
	Class   ErrorClass
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.Cause)
}

func (e *Error) Unwrap() error {
	return e.Cause
}

func newError(class ErrorClass, code, message string, cause error) *Error {
	return &Error{Class: class, Code: code, Message: message, Cause: cause}
}
