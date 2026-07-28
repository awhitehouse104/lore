package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"lore/internal/docs"
	"lore/internal/gitx"
	"lore/internal/id"
	"lore/internal/lock"
	"lore/internal/repository"
	"lore/internal/search"
	"lore/internal/transaction"
)

type Clock interface {
	Now() time.Time
}

type RealClock struct{}

func (RealClock) Now() time.Time {
	return time.Now()
}

type CaptureGit interface {
	CommitPath(context.Context, string, string, string) (string, error)
	PushHead(context.Context, string, string) error
}

type HistoryGit interface {
	IsRepository(context.Context, string) (bool, error)
	Recent(context.Context, string, int, bool) ([]gitx.Commit, error)
}

type TransactionHooks interface {
	AfterFileRename(index int, path string) error
	AfterGitCommit(commit string) error
}

type Service struct {
	Repo     *repository.Repository
	Git      CaptureGit
	Clock    Clock
	IDs      id.Generator
	Searcher search.Searcher
	History  HistoryGit
	TxGit    gitx.Client
	TxIDs    transaction.IDGenerator
	Actor    string
	TxHooks  TransactionHooks
}

func NewService(repo *repository.Repository) *Service {
	return &Service{
		Repo:     repo,
		Git:      gitx.New(),
		Clock:    RealClock{},
		IDs:      id.CryptoGenerator{},
		Searcher: search.FilesystemLexicalSearcher{},
		History:  gitx.New(),
		TxGit:    gitx.New(),
		TxIDs:    transaction.CryptoIDGenerator{},
		Actor:    transaction.DefaultActor,
	}
}

type CaptureOptions struct {
	Kind        string
	Origin      string
	OriginRef   string
	Sensitivity string
	Tags        []string
	Body        []byte
	AllowEmpty  bool
	NoCommit    bool
	Push        *bool
}

type CaptureResult struct {
	SchemaVersion int      `json:"schema_version"`
	ID            string   `json:"id"`
	Path          string   `json:"path"`
	URI           string   `json:"uri"`
	CapturedAt    string   `json:"captured_at"`
	RawSHA256     string   `json:"raw_sha256"`
	Bytes         int      `json:"bytes"`
	Written       bool     `json:"written"`
	Committed     bool     `json:"committed"`
	Commit        string   `json:"commit"`
	Pushed        bool     `json:"pushed"`
	Warnings      []string `json:"warnings"`
}

func (s *Service) Capture(ctx context.Context, options CaptureOptions) (result CaptureResult, returnErr error) {
	if s == nil || s.Repo == nil || s.Git == nil || s.Clock == nil || s.IDs == nil {
		return result, NewError(ExitRuntime, "service_unavailable", "capture service is not fully configured")
	}
	if !docs.ValidToken(options.Kind) {
		return result, NewError(ExitValidation, "invalid_kind", "kind must match ^[a-z][a-z0-9_-]*$")
	}
	if !docs.ValidToken(options.Origin) {
		return result, NewError(ExitValidation, "invalid_origin", "origin must match ^[a-z][a-z0-9_-]*$")
	}
	if options.Sensitivity == "" {
		options.Sensitivity = "normal"
	}
	if !docs.ValidSensitivity(options.Sensitivity) {
		return result, NewError(ExitValidation, "invalid_sensitivity", "sensitivity must be normal, sensitive, or local-only")
	}
	for _, tag := range options.Tags {
		if strings.TrimSpace(tag) == "" {
			return result, NewError(ExitValidation, "invalid_tag", "tags must be non-empty")
		}
	}
	if len(options.Body) == 0 && !options.AllowEmpty {
		return result, NewError(ExitValidation, "empty_capture", "capture body is empty; use --allow-empty to preserve an intentional empty source")
	}
	if int64(len(options.Body)) > s.Repo.Config.Capture.MaxBytes {
		return result, NewError(ExitValidation, "capture_too_large", fmt.Sprintf("capture body exceeds configured maximum of %d bytes", s.Repo.Config.Capture.MaxBytes))
	}
	if !utf8.Valid(options.Body) {
		return result, NewError(ExitValidation, "invalid_utf8", "capture body is not valid UTF-8")
	}

	now := s.Clock.Now().UTC()
	handle, err := lock.Acquire(s.Repo.Root, "capture", now)
	if err != nil {
		var contention *lock.ContentionError
		if errors.As(err, &contention) {
			return result, &APIError{
				Code:    "repository_locked",
				Message: contention.Error(),
				Details: map[string]any{
					"lock_path":       lock.ManualRecoveryPath(s.Repo.Root),
					"pid":             contention.Metadata.PID,
					"hostname":        contention.Metadata.Hostname,
					"command":         contention.Metadata.Command,
					"started_at":      contention.Metadata.StartedAt,
					"manual_recovery": "verify that the owning process has exited, then remove the lock directory manually",
				},
				ExitCode: ExitConflict,
				Cause:    err,
			}
		}
		apiErr := NewError(ExitRuntime, "lock_failed", "could not acquire repository write lock")
		apiErr.Cause = err
		return result, apiErr
	}
	defer func() {
		if releaseErr := handle.Release(); releaseErr != nil && returnErr == nil {
			apiErr := NewError(ExitRuntime, "lock_release_failed", "capture completed but the repository write lock could not be released")
			apiErr.Details = map[string]any{"lock_path": lock.ManualRecoveryPath(s.Repo.Root)}
			apiErr.Cause = releaseErr
			returnErr = apiErr
		}
	}()
	if apiErr := s.requireNoRecovery(); apiErr != nil {
		return result, apiErr
	}

	sourceID, err := s.IDs.New(now)
	if err != nil {
		apiErr := NewError(ExitRuntime, "id_generation_failed", "could not generate a source ID")
		apiErr.Cause = err
		return result, apiErr
	}
	if err := docs.ValidateSourceID(sourceID); err != nil {
		apiErr := NewError(ExitRuntime, "id_generation_failed", "source ID generator returned an invalid ID")
		apiErr.Cause = err
		return result, apiErr
	}
	rawHash := docs.SHA256(options.Body)
	source := docs.Source{
		ID:          sourceID,
		Kind:        options.Kind,
		CapturedAt:  docs.TimestampString(now.Format(time.RFC3339Nano)),
		Origin:      options.Origin,
		OriginRef:   options.OriginRef,
		RawSHA256:   rawHash,
		Sensitivity: options.Sensitivity,
		Tags:        append([]string(nil), options.Tags...),
	}
	data, err := docs.MarshalSource(source, options.Body)
	if err != nil {
		apiErr := NewError(ExitRuntime, "source_serialization_failed", "could not serialize source metadata")
		apiErr.Cause = err
		return result, apiErr
	}
	relative := filepath.ToSlash(filepath.Join(
		"sources", now.Format("2006"), now.Format("01"), sourceID+"-"+options.Kind+".md",
	))
	if err := s.Repo.AtomicCreate(relative, data); err != nil {
		var exists *repository.PathExistsError
		if errors.As(err, &exists) {
			return result, &APIError{
				Code:     "capture_conflict",
				Message:  "a captured source already exists at the generated path",
				Details:  map[string]any{"path": relative, "id": sourceID},
				ExitCode: ExitConflict,
				Cause:    err,
			}
		}
		apiErr := NewError(ExitRuntime, "capture_write_failed", "could not write captured source")
		apiErr.Details = map[string]any{"path": relative, "id": sourceID}
		apiErr.Cause = err
		return result, apiErr
	}
	result = CaptureResult{
		SchemaVersion: SchemaVersion,
		ID:            sourceID,
		Path:          relative,
		URI:           "lore://" + relative,
		CapturedAt:    now.Format(time.RFC3339Nano),
		RawSHA256:     rawHash,
		Bytes:         len(options.Body),
		Written:       true,
		Committed:     false,
		Commit:        "",
		Pushed:        false,
		Warnings:      []string{},
	}

	shouldCommit := s.Repo.Config.Git.AutoCommitCaptures && !options.NoCommit
	if !shouldCommit {
		return result, nil
	}
	subject := fmt.Sprintf("capture: %s %s", options.Kind, sourceID)
	commit, err := s.Git.CommitPath(ctx, s.Repo.Root, relative, subject)
	if err != nil {
		return result, &APIError{
			Code:    "git_commit_failed",
			Message: "source was safely written but its Git commit failed",
			Details: map[string]any{
				"id":       sourceID,
				"path":     relative,
				"written":  true,
				"recovery": "resolve the Git error, then commit only the captured source path",
			},
			ExitCode: ExitRuntime,
			Cause:    err,
		}
	}
	result.Committed = true
	result.Commit = commit

	shouldPush := s.Repo.Config.Git.AutoPushCaptures
	if options.Push != nil {
		shouldPush = *options.Push
	}
	if !shouldPush {
		return result, nil
	}
	if err := s.Git.PushHead(ctx, s.Repo.Root, s.Repo.Config.Git.Remote); err != nil {
		if s.Repo.Config.Git.RequirePush {
			return result, &APIError{
				Code:    "git_push_failed",
				Message: "source is safely committed locally but the required Git push failed",
				Details: map[string]any{
					"id":              sourceID,
					"path":            relative,
					"commit":          commit,
					"committed_local": true,
					"pushed":          false,
				},
				ExitCode: ExitRuntime,
				Cause:    err,
			}
		}
		result.Warnings = append(result.Warnings, fmt.Sprintf("Git push failed; source remains safely committed locally: %v", err))
		return result, nil
	}
	result.Pushed = true
	return result, nil
}
