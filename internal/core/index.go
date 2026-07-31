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

const indexRefreshWarning = "existing index refresh failed; run lore index update"

func (s *Service) bestEffortIndexRefresh(ctx context.Context) []string {
	if s == nil || s.Repo == nil || !s.Repo.Config.Index.AutoRefreshExisting || s.IndexMaintenance == nil {
		return nil
	}
	status, err := s.IndexMaintenance.Status(ctx, false)
	if err != nil {
		return []string{indexRefreshWarning}
	}
	switch status.IndexState {
	case loreindex.StateMissing:
		return nil
	case loreindex.StateCorrupt, loreindex.StateIncompatible, loreindex.StateBuilding:
		return []string{indexRefreshWarning}
	}
	if _, err := s.IndexMaintenance.Update(ctx); err != nil {
		return []string{indexRefreshWarning}
	}
	return nil
}

func (s *Service) IndexUpdate(ctx context.Context) (result loreindex.UpdateResult, returnErr error) {
	if s == nil || s.Repo == nil || s.Clock == nil {
		return result, NewError(ExitRuntime, "service_unavailable", "index service is not fully configured")
	}
	handle, apiErr := s.acquireWriteLock(ctx, "index-update", s.Clock.Now().UTC())
	if apiErr != nil {
		return result, apiErr
	}
	defer func() {
		if releaseErr := handle.Release(); releaseErr != nil && returnErr == nil {
			apiErr := NewError(ExitRuntime, "lock_release_failed", "index update completed but the repository write lock could not be released")
			apiErr.Cause = releaseErr
			returnErr = apiErr
		}
	}()
	if apiErr := s.requireNoRecovery(); apiErr != nil {
		return result, apiErr
	}
	if apiErr := s.requireCleanManagedIndexSnapshot(ctx); apiErr != nil {
		return result, apiErr
	}
	lintResult, err := lint.RunAt(ctx, s.Repo, s.TxGit, s.Clock.Now().UTC())
	if err != nil {
		return result, indexOperationError("lint_failed", "could not validate canonical documents before index update", err)
	}
	if !lintResult.Valid {
		apiErr := NewError(ExitValidation, "invalid_canonical_documents", "canonical document errors prevent index update")
		apiErr.Details = map[string]any{"errors": lintResult.Errors, "findings": lintResult.Findings}
		return result, apiErr
	}
	result, err = s.indexManager().Update(ctx)
	if err != nil {
		return result, mapIndexError(err)
	}
	return result, nil
}

func (s *Service) IndexClear(ctx context.Context) (result loreindex.ClearResult, returnErr error) {
	if s == nil || s.Repo == nil || s.Clock == nil {
		return result, NewError(ExitRuntime, "service_unavailable", "index service is not fully configured")
	}
	handle, apiErr := s.acquireWriteLock(ctx, "index-clear", s.Clock.Now().UTC())
	if apiErr != nil {
		return result, apiErr
	}
	defer func() {
		if releaseErr := handle.Release(); releaseErr != nil && returnErr == nil {
			apiErr := NewError(ExitRuntime, "lock_release_failed", "index clear completed but the repository write lock could not be released")
			apiErr.Cause = releaseErr
			returnErr = apiErr
		}
	}()
	result, err := s.indexManager().Clear()
	if err != nil {
		return result, mapIndexError(err)
	}
	return result, nil
}

func (s *Service) IndexBuild(ctx context.Context, options IndexBuildOptions) (result loreindex.BuildResult, returnErr error) {
	if s == nil || s.Repo == nil || s.Clock == nil {
		return result, NewError(ExitRuntime, "service_unavailable", "index service is not fully configured")
	}
	handle, apiErr := s.acquireWriteLock(ctx, "index-build", s.Clock.Now().UTC())
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
	if apiErr := s.requireCleanManagedIndexSnapshot(ctx); apiErr != nil {
		return result, apiErr
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

func (s *Service) requireCleanManagedIndexSnapshot(ctx context.Context) *APIError {
	isGit, err := s.TxGit.IsRepository(ctx, s.Repo.Root)
	if err != nil {
		return indexOperationError("git_repository_check_failed", "could not inspect the Git repository", err)
	}
	if !isGit {
		return nil
	}
	changes, err := s.TxGit.Changes(ctx, s.Repo.Root, []string{"pages", "sources"})
	if err != nil {
		return indexOperationError("git_status_failed", "could not inspect managed Git paths", err)
	}
	if len(changes) == 0 {
		return nil
	}
	apiErr := NewError(ExitConflict, "managed_worktree_dirty", "index maintenance requires clean tracked and untracked pages and sources")
	apiErr.Details = map[string]any{"changes": changes}
	return apiErr
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
	if manager, ok := s.IndexMaintenance.(*loreindex.Manager); ok && manager != nil {
		if s.Clock != nil {
			manager.Clock = s.Clock
		}
		return manager
	}
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
