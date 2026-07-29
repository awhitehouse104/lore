package core

import (
	"context"
	"errors"

	loreindex "lore/internal/index"
	"lore/internal/lint"
	"lore/internal/version"
)

type IndexBuildOptions struct {
	Force bool
}

func (s *Service) IndexBuild(ctx context.Context, options IndexBuildOptions) (result loreindex.BuildResult, returnErr error) {
	if s == nil || s.Repo == nil || s.Clock == nil {
		return result, NewError(ExitRuntime, "service_unavailable", "index service is not fully configured")
	}
	handle, apiErr := acquireWriteLock(s.Repo, "index-build", s.Clock.Now().UTC())
	if apiErr != nil {
		return result, apiErr
	}
	defer func() {
		if releaseErr := handle.Release(); releaseErr != nil && returnErr == nil {
			apiErr := NewError(ExitRuntime, "lock_release_failed", "index build completed but the repository write lock could not be released")
			apiErr.Cause = releaseErr
			returnErr = apiErr
		}
	}()
	if apiErr := s.requireNoRecovery(); apiErr != nil {
		return result, apiErr
	}
	isGit, err := s.TxGit.IsRepository(ctx, s.Repo.Root)
	if err != nil {
		return result, indexOperationError("git_repository_check_failed", "could not inspect the Git repository", err)
	}
	if isGit {
		changes, err := s.TxGit.Changes(ctx, s.Repo.Root, []string{"pages", "sources"})
		if err != nil {
			return result, indexOperationError("git_status_failed", "could not inspect managed Git paths", err)
		}
		if len(changes) > 0 {
			apiErr := NewError(ExitConflict, "managed_worktree_dirty", "index build requires clean tracked and untracked pages and sources")
			apiErr.Details = map[string]any{"changes": changes}
			return result, apiErr
		}
	}
	lintResult, err := lint.RunAt(ctx, s.Repo, s.TxGit, s.Clock.Now().UTC())
	if err != nil {
		return result, indexOperationError("lint_failed", "could not validate canonical documents before indexing", err)
	}
	if !lintResult.Valid {
		apiErr := NewError(ExitValidation, "invalid_canonical_documents", "canonical document errors prevent index build")
		apiErr.Details = map[string]any{"errors": lintResult.Errors, "findings": lintResult.Findings}
		return result, apiErr
	}
	manager := s.indexManager()
	result, err = manager.Build(ctx, loreindex.BuildOptions{Force: options.Force})
	if err != nil {
		return result, mapIndexError(err)
	}
	return result, nil
}

func (s *Service) IndexStatus(ctx context.Context, verify bool) (loreindex.Status, error) {
	if s == nil || s.Repo == nil {
		return loreindex.Status{}, NewError(ExitRuntime, "service_unavailable", "index service is not fully configured")
	}
	result, err := s.indexManager().Status(ctx, verify)
	if err != nil {
		return result, mapIndexError(err)
	}
	return result, nil
}

func (s *Service) indexManager() *loreindex.Manager {
	manager := loreindex.NewManager(s.Repo, s.TxGit, version.Version)
	if s.Clock != nil {
		manager.Clock = s.Clock
	}
	return manager
}

func mapIndexError(err error) *APIError {
	var indexErr *loreindex.Error
	if errors.As(err, &indexErr) {
		exitCode := ExitRuntime
		switch indexErr.Class {
		case loreindex.ErrorValidation:
			exitCode = ExitValidation
		case loreindex.ErrorUsage:
			exitCode = ExitUsage
		case loreindex.ErrorConflict:
			exitCode = ExitConflict
		}
		apiErr := NewError(exitCode, indexErr.Code, indexErr.Message)
		apiErr.Cause = indexErr.Cause
		return apiErr
	}
	return indexOperationError("index_operation_failed", "index operation failed", err)
}

func indexOperationError(code, message string, cause error) *APIError {
	apiErr := NewError(ExitRuntime, code, message)
	apiErr.Cause = cause
	return apiErr
}
