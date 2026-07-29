package core

import (
	"context"
	"strings"

	"lore/internal/docs"
	"lore/internal/lint"
	"lore/internal/repository"
	"lore/internal/search"
)

// Lint runs the canonical repository validation behind the same typed service
// boundary used by CLI and protocol adapters.
func (s *Service) Lint(ctx context.Context) (lint.Result, error) {
	if s == nil || s.Repo == nil {
		return lint.Result{}, NewError(ExitRuntime, "service_unavailable", "lint service is not fully configured")
	}
	result, err := lint.Run(ctx, s.Repo, s.TxGit)
	if err != nil {
		apiErr := NewError(ExitRuntime, "lint_failed", "could not lint the repository")
		apiErr.Cause = err
		return lint.Result{}, apiErr
	}
	return result, nil
}

type AuthorizedLintResult struct {
	Result                       lint.Result
	AdditionalInaccessibleErrors bool
}

func (s *Service) LintAuthorized(ctx context.Context, access search.AccessPolicy) (AuthorizedLintResult, error) {
	if access.AllowedSensitivities == nil {
		return AuthorizedLintResult{}, NewError(ExitUsage, "access_policy_required", "lint requires an explicit sensitivity access policy")
	}
	result, err := s.Lint(ctx)
	if err != nil {
		return AuthorizedLintResult{}, err
	}
	return filterLintResult(ctx, repository.FilesystemView{Repository: s.Repo}, result, access)
}

func filterLintResult(
	ctx context.Context,
	view repository.View,
	result lint.Result,
	access search.AccessPolicy,
) (AuthorizedLintResult, error) {
	paths, _, err := view.ManagedMarkdown()
	if err != nil {
		apiErr := NewError(ExitRuntime, "catalog_scan_failed", "could not classify lint diagnostics")
		apiErr.Cause = err
		return AuthorizedLintResult{}, apiErr
	}
	allowedPaths := make(map[string]bool, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return AuthorizedLintResult{}, err
		}
		data, err := view.ReadFile(path)
		if err != nil {
			continue
		}
		document, err := docs.Parse(path, data)
		if err == nil {
			allowedPaths[path] = access.Allows(document.Sensitivity())
		}
	}
	filtered := result
	filtered.Findings = make([]lint.Finding, 0, len(result.Findings))
	filtered.Errors = 0
	filtered.Warnings = 0
	additionalErrors := false
	for _, finding := range result.Findings {
		if !lintFindingAllowed(finding, allowedPaths) {
			if finding.Severity == lint.SeverityError {
				additionalErrors = true
			}
			continue
		}
		filtered.Findings = append(filtered.Findings, finding)
		if finding.Severity == lint.SeverityError {
			filtered.Errors++
		} else {
			filtered.Warnings++
		}
	}
	return AuthorizedLintResult{
		Result:                       filtered,
		AdditionalInaccessibleErrors: additionalErrors,
	}, nil
}

func lintFindingAllowed(finding lint.Finding, allowedPaths map[string]bool) bool {
	paths := append([]string{finding.Path}, finding.RelatedPaths...)
	for _, path := range paths {
		if !strings.HasPrefix(path, "pages/") && !strings.HasPrefix(path, "sources/") {
			continue
		}
		allowed, known := allowedPaths[path]
		if !known || !allowed {
			return false
		}
	}
	return true
}
