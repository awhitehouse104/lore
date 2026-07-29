package core

import (
	"context"

	"lore/internal/lint"
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
