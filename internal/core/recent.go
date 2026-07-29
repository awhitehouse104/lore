package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"lore/internal/docs"
	"lore/internal/gitx"
	"lore/internal/search"
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

type historyMetadataGit interface {
	ChangedPathsInCommit(context.Context, string, string) ([]string, error)
	BlobAtCommit(context.Context, string, string, string) ([]byte, error)
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

func (s *Service) RecentAuthorized(ctx context.Context, options RecentOptions, access search.AccessPolicy) (RecentResult, error) {
	if access.AllowedSensitivities == nil {
		return RecentResult{}, NewError(ExitUsage, "access_policy_required", "recent history requires an explicit sensitivity access policy")
	}
	limit := options.Limit
	if limit == 0 {
		limit = DefaultRecentLimit
	}
	if limit < 1 || limit > MaximumRecentLimit {
		return RecentResult{}, NewError(ExitUsage, "invalid_limit", fmt.Sprintf("recent limit must be between 1 and %d", MaximumRecentLimit))
	}
	rawOptions := options
	rawOptions.Limit = MaximumRecentLimit
	raw, err := s.Recent(ctx, rawOptions)
	if err != nil {
		return RecentResult{}, err
	}
	classifier, ok := s.History.(historyMetadataGit)
	if !ok {
		if allowsAllSensitivities(access) {
			if len(raw.Commits) > limit {
				raw.Commits = raw.Commits[:limit]
			}
			return raw, nil
		}
		return RecentResult{SchemaVersion: SchemaVersion, Commits: []gitx.Commit{}}, nil
	}
	filtered := make([]gitx.Commit, 0, min(limit, len(raw.Commits)))
	for _, commit := range raw.Commits {
		allowed, denied, classifiable, err := classifyCommit(ctx, classifier, s.Repo.Root, commit.Hash, access)
		if err != nil {
			apiErr := NewError(ExitRuntime, "git_history_classification_failed", "could not safely classify Git history")
			apiErr.Cause = err
			return RecentResult{}, apiErr
		}
		if !classifiable || !allowed {
			continue
		}
		if denied {
			commit.Subject = "Lore knowledge changed"
		}
		filtered = append(filtered, commit)
		if len(filtered) == limit {
			break
		}
	}
	return RecentResult{SchemaVersion: SchemaVersion, Commits: filtered}, nil
}

func classifyCommit(
	ctx context.Context,
	history historyMetadataGit,
	root, commit string,
	access search.AccessPolicy,
) (allowed, denied, classifiable bool, returnErr error) {
	paths, err := history.ChangedPathsInCommit(ctx, root, commit)
	if err != nil {
		return false, false, false, err
	}
	managed := 0
	for _, path := range paths {
		if (!strings.HasPrefix(path, "pages/") && !strings.HasPrefix(path, "sources/")) || !strings.HasSuffix(path, ".md") {
			continue
		}
		managed++
		data, err := history.BlobAtCommit(ctx, root, commit, path)
		if err != nil {
			return false, false, false, nil
		}
		document, err := docs.Parse(path, data)
		if err != nil {
			return false, false, false, nil
		}
		if access.Allows(document.Sensitivity()) {
			allowed = true
		} else {
			denied = true
		}
	}
	return allowed, denied, managed > 0, nil
}

func allowsAllSensitivities(access search.AccessPolicy) bool {
	return access.Allows("normal") && access.Allows("sensitive") && access.Allows("local-only")
}
