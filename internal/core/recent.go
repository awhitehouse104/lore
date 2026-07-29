package core

import (
	"context"
	"fmt"
	"time"

	"lore/internal/gitx"
)

const (
	DefaultRecentLimit = 20
	MaximumRecentLimit = 200
)

type RecentOptions struct {
	Limit int
	All   bool
	Since *time.Time
}

type RecentResult struct {
	SchemaVersion int           `json:"schema_version"`
	Commits       []gitx.Commit `json:"commits"`
}

func (s *Service) Recent(ctx context.Context, options RecentOptions) (RecentResult, error) {
	if s == nil || s.Repo == nil || s.History == nil {
		return RecentResult{}, NewError(ExitRuntime, "service_unavailable", "recent-history service is not fully configured")
	}
	if options.Limit == 0 {
		options.Limit = DefaultRecentLimit
	}
	if options.Limit < 1 || options.Limit > MaximumRecentLimit {
		return RecentResult{}, NewError(ExitUsage, "invalid_limit", fmt.Sprintf("recent limit must be between 1 and %d", MaximumRecentLimit))
	}
	isGit, err := s.History.IsRepository(ctx, s.Repo.Root)
	if err != nil {
		apiErr := NewError(ExitRuntime, "git_repository_check_failed", "could not inspect the repository's Git state")
		apiErr.Cause = err
		return RecentResult{}, apiErr
	}
	if !isGit {
		return RecentResult{}, NewError(ExitRuntime, "git_repository_required", "lore recent requires a Git repository")
	}
	fetchLimit := options.Limit
	if options.Since != nil {
		fetchLimit = MaximumRecentLimit
	}
	commits, err := s.History.Recent(ctx, s.Repo.Root, fetchLimit, !options.All)
	if err != nil {
		apiErr := NewError(ExitRuntime, "git_history_failed", "could not read Git history")
		apiErr.Cause = err
		return RecentResult{}, apiErr
	}
	if options.Since != nil {
		since := options.Since.UTC()
		filtered := commits[:0]
		for _, commit := range commits {
			if commit.CommittedAt.Before(since) {
				continue
			}
			filtered = append(filtered, commit)
			if len(filtered) == options.Limit {
				break
			}
		}
		commits = filtered
	}
	return RecentResult{
		SchemaVersion: SchemaVersion,
		Commits:       commits,
	}, nil
}
