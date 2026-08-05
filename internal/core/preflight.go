package core

import (
	"context"
	"fmt"
	"time"

	"lore/internal/gitx"
	loreindex "lore/internal/index"
	"lore/internal/lint"
	"lore/internal/lock"
	"lore/internal/transaction"
)

const DefaultPreflightBranch = "main"

type PreflightOptions struct {
	Sync   bool
	Deep   bool
	Branch string
}

type PreflightChange struct {
	Status string `json:"status"`
	Path   string `json:"path"`
}

type PreflightBlocker struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Action  string            `json:"action,omitempty"`
	Count   int               `json:"count,omitempty"`
	Ahead   int               `json:"ahead,omitempty"`
	Behind  int               `json:"behind,omitempty"`
	Changes []PreflightChange `json:"changes,omitempty"`
}

type PreflightLocal struct {
	Branch         string            `json:"branch"`
	Detached       bool              `json:"detached"`
	WorktreeClean  bool              `json:"worktree_clean"`
	Changes        []PreflightChange `json:"changes"`
	RecoveryActive bool              `json:"recovery_active"`
	RecoveryAction string            `json:"recovery_action"`
	PendingPreview bool              `json:"pending_preview"`
}

type PreflightRemote struct {
	Checked       bool   `json:"checked"`
	Remote        string `json:"remote"`
	Branch        string `json:"branch"`
	Ahead         int    `json:"ahead"`
	Behind        int    `json:"behind"`
	FastForwarded bool   `json:"fast_forwarded"`
}

type PreflightTiming struct {
	Stage      string `json:"stage"`
	DurationMS int64  `json:"duration_ms"`
}

type PreflightResult struct {
	SchemaVersion int                `json:"schema_version"`
	Status        string             `json:"status"`
	Scope         string             `json:"scope"`
	Ready         bool               `json:"ready"`
	HeadBefore    string             `json:"head_before"`
	HeadAfter     string             `json:"head_after"`
	Local         PreflightLocal     `json:"local"`
	Remote        PreflightRemote    `json:"remote"`
	Lint          *lint.Result       `json:"lint,omitempty"`
	Index         *loreindex.Status  `json:"index,omitempty"`
	IndexAction   string             `json:"index_action"`
	Blockers      []PreflightBlocker `json:"blockers"`
	Timings       []PreflightTiming  `json:"timings"`
	DurationMS    int64              `json:"duration_ms"`
}

func (s *Service) Preflight(ctx context.Context, options PreflightOptions) (result PreflightResult, returnErr error) {
	started := time.Now()
	result = newPreflightResult(s, options)
	defer func() {
		result.DurationMS = elapsedMilliseconds(started)
	}()
	if s == nil || s.Repo == nil || s.Clock == nil {
		return result, NewError(ExitRuntime, "service_unavailable", "preflight service is not fully configured")
	}
	if err := s.TxGit.ValidateBranch(ctx, s.Repo.Root, result.Remote.Branch); err != nil {
		apiErr := NewError(ExitUsage, "invalid_branch", "preflight branch is not a valid Git branch name")
		apiErr.Cause = err
		return result, apiErr
	}
	isRepository, err := s.TxGit.IsRepository(ctx, s.Repo.Root)
	if err != nil {
		return result, preflightRuntimeError("git_repository_check_failed", "could not inspect the Git repository", err)
	}
	if !isRepository {
		result.addBlocker("git_repository_required", "preflight requires the Lore root to be a Git repository", "initialize or repair the Git repository")
		return result, nil
	}

	handle, apiErr := s.acquireWriteLock(ctx, "preflight", s.Clock.Now().UTC())
	if apiErr != nil {
		return result, apiErr
	}
	defer func() {
		if releaseErr := handle.Release(); releaseErr != nil && returnErr == nil {
			apiErr := NewError(ExitRuntime, "lock_release_failed", "preflight completed but the repository write lock could not be released")
			apiErr.Details = map[string]any{"lock_path": lock.Path(s.Repo.Root)}
			apiErr.Cause = releaseErr
			returnErr = apiErr
		}
	}()

	if err := s.runPreflightStage(&result, "local_safety", func() error {
		return s.inspectPreflightLocal(ctx, &result)
	}); err != nil {
		return result, err
	}
	if len(result.Blockers) > 0 {
		return result, nil
	}

	if options.Sync {
		if err := s.runPreflightStage(&result, "fetch", func() error {
			return s.TxGit.FetchBranch(ctx, s.Repo.Root, result.Remote.Remote, result.Remote.Branch)
		}); err != nil {
			return result, preflightRuntimeError("git_fetch_failed", "could not fetch the configured Lore branch", err)
		}
		result.Remote.Checked = true
		counts, err := s.TxGit.AheadBehind(ctx, s.Repo.Root, result.Remote.Remote, result.Remote.Branch)
		if err != nil {
			return result, preflightRuntimeError("git_comparison_failed", "could not compare local and fetched Lore history", err)
		}
		result.Remote.Ahead = counts.Ahead
		result.Remote.Behind = counts.Behind
		switch {
		case counts.Ahead > 0 && counts.Behind > 0:
			result.addHistoryBlocker("git_diverged", "local and remote Lore histories have diverged", "stop and reconcile the histories explicitly", counts)
		case counts.Ahead > 0:
			result.addHistoryBlocker("git_ahead", "the local Lore clone contains commits not present on the remote", "push or otherwise reconcile the local commits before continuing", counts)
		case counts.Behind > 0:
			if err := s.runPreflightStage(&result, "fast_forward", func() error {
				return s.TxGit.FastForward(ctx, s.Repo.Root, result.Remote.Remote, result.Remote.Branch)
			}); err != nil {
				return result, preflightRuntimeError("git_fast_forward_failed", "could not fast-forward to the fetched Lore branch", err)
			}
			result.Remote.FastForwarded = true
		}
		if len(result.Blockers) > 0 {
			return result, nil
		}
		result.HeadAfter, err = s.TxGit.Head(ctx, s.Repo.Root)
		if err != nil {
			return result, preflightRuntimeError("git_head_failed", "could not inspect Git HEAD after synchronization", err)
		}
		if result.Remote.FastForwarded {
			changes, err := s.TxGit.Changes(ctx, s.Repo.Root, nil)
			if err != nil {
				return result, preflightRuntimeError("git_status_failed", "could not recheck the worktree after fast-forward", err)
			}
			if len(changes) > 0 {
				result.Local.WorktreeClean = false
				result.Local.Changes = preflightChanges(changes)
				result.addChangesBlocker("worktree_changed", "the Lore worktree changed during preflight", "stop and inspect the worktree before continuing", changes)
				return result, nil
			}
		}
	}

	headChanged := result.HeadBefore != result.HeadAfter
	if err := s.reconcilePreflightIndex(ctx, &result, headChanged || options.Deep); err != nil {
		return result, err
	}
	if len(result.Blockers) == 0 {
		result.Ready = true
		result.Status = "ready"
	}
	return result, nil
}

func newPreflightResult(s *Service, options PreflightOptions) PreflightResult {
	branch := options.Branch
	if branch == "" {
		branch = DefaultPreflightBranch
	}
	remote := ""
	if s != nil && s.Repo != nil {
		remote = s.Repo.Config.Git.Remote
	}
	scope := "local"
	if options.Sync {
		scope = "synchronized"
	}
	return PreflightResult{
		SchemaVersion: SchemaVersion,
		Status:        "blocked",
		Scope:         scope,
		Local: PreflightLocal{
			Changes: []PreflightChange{},
		},
		Remote:      PreflightRemote{Remote: remote, Branch: branch},
		IndexAction: "none",
		Blockers:    []PreflightBlocker{},
		Timings:     []PreflightTiming{},
	}
}

func (s *Service) inspectPreflightLocal(ctx context.Context, result *PreflightResult) error {
	branch, detached, err := s.TxGit.BranchState(ctx, s.Repo.Root)
	if err != nil {
		return preflightRuntimeError("git_branch_failed", "could not inspect the current Git branch", err)
	}
	result.Local.Branch = branch
	result.Local.Detached = detached
	if detached {
		result.addBlocker("detached_head", "the Lore repository has a detached Git HEAD", "switch to the configured session branch before continuing")
	} else if branch != result.Remote.Branch {
		result.addBlocker("wrong_branch", fmt.Sprintf("the Lore repository is on branch %q instead of %q", branch, result.Remote.Branch), "switch to the configured session branch before continuing")
	}
	result.HeadBefore, err = s.TxGit.Head(ctx, s.Repo.Root)
	if err != nil {
		return preflightRuntimeError("git_head_failed", "could not inspect Git HEAD", err)
	}
	result.HeadAfter = result.HeadBefore

	changes, err := s.TxGit.Changes(ctx, s.Repo.Root, nil)
	if err != nil {
		return preflightRuntimeError("git_status_failed", "could not inspect the Lore worktree", err)
	}
	result.Local.WorktreeClean = len(changes) == 0
	result.Local.Changes = preflightChanges(changes)
	if len(changes) > 0 {
		result.addChangesBlocker("worktree_dirty", "the Lore worktree contains tracked or untracked changes", "stop and inspect or preserve the changes before synchronizing", changes)
	}

	recovery, err := s.RecoveryStatus(ctx)
	if err != nil {
		return err
	}
	result.Local.RecoveryActive = recovery.Active
	result.Local.RecoveryAction = recovery.RecommendedAction
	if recovery.Active {
		result.addBlocker("recovery_required", "an active recovery journal blocks a new session", recovery.RecommendedAction)
	}

	previews, err := s.TransactionList(transaction.StatusPreviewed, 1)
	if err != nil {
		return err
	}
	result.Local.PendingPreview = len(previews.Transactions) > 0
	if len(previews.Transactions) > 0 {
		result.addBlocker("pending_preview", "a previewed transaction must be committed or discarded before synchronization", "inspect the pending transaction before continuing")
	}
	return nil
}

func (s *Service) reconcilePreflightIndex(ctx context.Context, result *PreflightResult, verify bool) error {
	manager := s.indexManager()
	status, err := manager.Status(ctx, false)
	if err != nil {
		return mapIndexError(err)
	}
	result.Index = &status

	needsLint := verify || status.IndexState == loreindex.StateMissing || status.IndexState == loreindex.StateStale
	if needsLint {
		var lintResult lint.Result
		if err := s.runPreflightStage(result, "lint", func() error {
			var lintErr error
			lintResult, lintErr = lint.RunAt(ctx, s.Repo, s.TxGit, s.Clock.Now().UTC())
			return lintErr
		}); err != nil {
			return preflightRuntimeError("lint_failed", "could not validate canonical documents during preflight", err)
		}
		result.Lint = &lintResult
		if !lintResult.Valid {
			result.addCountBlocker("lint_invalid", "canonical document errors block the Lore session", "repair the lint errors before continuing", lintResult.Errors)
			return nil
		}
	}

	switch status.IndexState {
	case loreindex.StateMissing:
		if err := s.runPreflightStage(result, "index_build", func() error {
			_, buildErr := manager.Build(ctx, loreindex.BuildOptions{})
			return buildErr
		}); err != nil {
			return mapIndexError(err)
		}
		result.IndexAction = "built"
		verify = true
	case loreindex.StateStale:
		if err := s.runPreflightStage(result, "index_update", func() error {
			_, updateErr := manager.Update(ctx)
			return updateErr
		}); err != nil {
			return mapIndexError(err)
		}
		result.IndexAction = "updated"
		verify = true
	case loreindex.StateFresh:
		result.IndexAction = "checked"
	case loreindex.StateBuilding:
		result.addBlocker("index_busy", "an index build or update is already active", "retry preflight after the index operation completes")
		return nil
	case loreindex.StateCorrupt, loreindex.StateIncompatible, loreindex.StateUncertified:
		result.addBlocker("index_unhealthy", fmt.Sprintf("the Lore index is %s", status.IndexState), "inspect the index and rebuild it before continuing")
		return nil
	default:
		result.addBlocker("index_unhealthy", "the Lore index has an unknown state", "inspect the index before continuing")
		return nil
	}

	if verify {
		if err := s.runPreflightStage(result, "index_verify", func() error {
			var statusErr error
			status, statusErr = manager.Status(ctx, true)
			return statusErr
		}); err != nil {
			return mapIndexError(err)
		}
		result.Index = &status
		if status.IndexState != loreindex.StateFresh {
			result.addBlocker("index_unhealthy", fmt.Sprintf("the Lore index is %s after reconciliation", status.IndexState), "inspect the index before continuing")
		}
	}
	if result.Index != nil && result.Index.IndexState == loreindex.StateFresh && result.Lint != nil && result.IndexAction == "updated" {
		filtered := lintWithoutFinding(*result.Lint, "index_stale")
		result.Lint = &filtered
	}
	return nil
}

func lintWithoutFinding(result lint.Result, code string) lint.Result {
	findings := make([]lint.Finding, 0, len(result.Findings))
	for _, finding := range result.Findings {
		if finding.Code == code {
			if finding.Severity == lint.SeverityError {
				result.Errors--
			} else if finding.Severity == lint.SeverityWarning {
				result.Warnings--
			}
			continue
		}
		findings = append(findings, finding)
	}
	result.Findings = findings
	result.Valid = result.Errors == 0
	return result
}

func (s *Service) runPreflightStage(result *PreflightResult, stage string, operation func() error) error {
	started := time.Now()
	err := operation()
	result.Timings = append(result.Timings, PreflightTiming{Stage: stage, DurationMS: elapsedMilliseconds(started)})
	return err
}

func (result *PreflightResult) addBlocker(code, message, action string) {
	result.Blockers = append(result.Blockers, PreflightBlocker{Code: code, Message: message, Action: action})
}

func (result *PreflightResult) addCountBlocker(code, message, action string, count int) {
	result.Blockers = append(result.Blockers, PreflightBlocker{Code: code, Message: message, Action: action, Count: count})
}

func (result *PreflightResult) addHistoryBlocker(code, message, action string, counts gitx.AheadBehind) {
	result.Blockers = append(result.Blockers, PreflightBlocker{Code: code, Message: message, Action: action, Ahead: counts.Ahead, Behind: counts.Behind})
}

func (result *PreflightResult) addChangesBlocker(code, message, action string, changes []gitx.Change) {
	result.Blockers = append(result.Blockers, PreflightBlocker{Code: code, Message: message, Action: action, Count: len(changes), Changes: preflightChanges(changes)})
}

func preflightChanges(changes []gitx.Change) []PreflightChange {
	result := make([]PreflightChange, 0, len(changes))
	for _, change := range changes {
		result = append(result, PreflightChange{Status: change.Status, Path: change.Path})
	}
	return result
}

func elapsedMilliseconds(started time.Time) int64 {
	duration := time.Since(started).Milliseconds()
	if duration < 0 {
		return 0
	}
	return duration
}

func preflightRuntimeError(code, message string, cause error) *APIError {
	apiErr := NewError(ExitRuntime, code, message)
	apiErr.Cause = cause
	return apiErr
}
