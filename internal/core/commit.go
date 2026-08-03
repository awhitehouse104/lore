package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"time"

	"lore/internal/diff"
	"lore/internal/docs"
	"lore/internal/lint"
	"lore/internal/recovery"
	"lore/internal/repository"
	"lore/internal/search"
	"lore/internal/transaction"
)

type CommitOptions struct {
	TransactionID string
	PreviewDigest string
	Push          *bool
}

type CommitResult struct {
	SchemaVersion    int                `json:"schema_version"`
	TransactionID    string             `json:"transaction_id"`
	Status           transaction.Status `json:"status"`
	PreviewDigest    string             `json:"preview_digest"`
	Commit           string             `json:"commit"`
	ChangedPaths     []string           `json:"changed_paths"`
	CommittedAt      string             `json:"committed_at"`
	Pushed           bool               `json:"pushed"`
	AlreadyCommitted bool               `json:"already_committed"`
	Warnings         []string           `json:"warnings"`
}

func (s *Service) Commit(ctx context.Context, options CommitOptions) (result CommitResult, returnErr error) {
	return s.commit(ctx, options, nil)
}

func (s *Service) CommitAuthorized(ctx context.Context, options CommitOptions, access search.AccessPolicy) (result CommitResult, returnErr error) {
	if access.AllowedSensitivities == nil {
		return result, NewError(ExitUsage, "access_policy_required", "transaction commit requires an explicit sensitivity access policy")
	}
	return s.commit(ctx, options, &access)
}

func (s *Service) commit(ctx context.Context, options CommitOptions, access *search.AccessPolicy) (result CommitResult, returnErr error) {
	if s == nil || s.Repo == nil || s.Clock == nil {
		return result, NewError(ExitRuntime, "service_unavailable", "transaction service is not fully configured")
	}
	if err := transaction.ValidateTransactionID(options.TransactionID); err != nil {
		return result, NewError(ExitUsage, "invalid_transaction_id", err.Error())
	}
	if err := transaction.ValidateRevision(options.PreviewDigest); err != nil {
		return result, NewError(ExitUsage, "invalid_preview_digest", "--preview-digest must be a lowercase SHA-256 value")
	}
	store, err := transaction.NewStore(s.Repo)
	if err != nil {
		return result, transactionRuntimeError("transaction_store_failed", "could not open the transaction store", err)
	}
	artifacts, err := store.Load(options.TransactionID)
	if err != nil {
		return result, transactionRuntimeError("transaction_integrity_failed", fmt.Sprintf("transaction %s failed integrity verification", options.TransactionID), err)
	}
	if !transaction.DigestEqual(options.PreviewDigest, artifacts.PreviewDigest) {
		return result, digestConflict(options.TransactionID)
	}
	if apiErr := s.validateCommitActor(artifacts.Proposal.Actor); apiErr != nil {
		if access != nil {
			return result, transactionNotFound()
		}
		return result, apiErr
	}
	if access != nil && !s.transactionArtifactsAuthorized(ctx, artifacts, *access) {
		return result, transactionNotFound()
	}
	if artifacts.State.Status == transaction.StatusCommitted {
		return committedResult(artifacts, true), nil
	}
	if artifacts.State.Status != transaction.StatusPreviewed {
		apiErr := NewError(ExitConflict, "transaction_not_committable", fmt.Sprintf("transaction %s is in state %s", options.TransactionID, artifacts.State.Status))
		apiErr.Details = map[string]any{"transaction_id": options.TransactionID, "status": artifacts.State.Status}
		return result, apiErr
	}

	now := s.Clock.Now().UTC()
	handle, apiErr := s.acquireWriteLock(ctx, "commit", now)
	if apiErr != nil {
		return result, apiErr
	}
	defer func() {
		if releaseErr := handle.Release(); releaseErr != nil && returnErr == nil {
			apiErr := NewError(ExitRuntime, "lock_release_failed", "transaction committed but the repository write lock could not be released")
			apiErr.Details = map[string]any{"transaction_id": options.TransactionID}
			apiErr.Cause = releaseErr
			returnErr = apiErr
		}
	}()

	if apiErr := s.requireNoRecovery(); apiErr != nil {
		return result, apiErr
	}
	artifacts, err = store.Load(options.TransactionID)
	if err != nil {
		return result, transactionRuntimeError("transaction_integrity_failed", fmt.Sprintf("transaction %s failed integrity verification", options.TransactionID), err)
	}
	if !transaction.DigestEqual(options.PreviewDigest, artifacts.PreviewDigest) {
		return result, digestConflict(options.TransactionID)
	}
	if apiErr := s.validateCommitActor(artifacts.Proposal.Actor); apiErr != nil {
		if access != nil {
			return result, transactionNotFound()
		}
		return result, apiErr
	}
	if access != nil && !s.transactionArtifactsAuthorized(ctx, artifacts, *access) {
		return result, transactionNotFound()
	}
	if artifacts.State.Status == transaction.StatusCommitted {
		return committedResult(artifacts, true), nil
	}
	if artifacts.State.Status != transaction.StatusPreviewed {
		return result, NewError(ExitConflict, "transaction_not_committable", fmt.Sprintf("transaction %s is in state %s", options.TransactionID, artifacts.State.Status))
	}
	if apiErr := s.verifyCommitBase(ctx, artifacts.Proposal); apiErr != nil {
		return result, apiErr
	}
	targetChanges, err := s.TxGit.Changes(ctx, s.Repo.Root, artifacts.Proposal.ChangedPaths)
	if err != nil {
		return result, transactionRuntimeError("git_status_failed", "could not inspect transaction target paths", err)
	}
	if len(targetChanges) > 0 {
		apiErr := NewError(ExitConflict, "target_path_dirty", "one or more transaction target paths have staged or unstaged changes")
		apiErr.Details = map[string]any{"changes": targetChanges}
		return result, apiErr
	}
	originals, originalExists, diffChanges, apiErr := s.verifyCommitTargets(artifacts)
	if apiErr != nil {
		return result, apiErr
	}
	overlayFiles := make(map[string][]byte, len(artifacts.Proposal.Operations))
	for index, operation := range artifacts.Proposal.Operations {
		overlayFiles[operation.Path] = artifacts.Contents[index]
	}
	view, err := repository.NewOverlayView(s.Repo, nil, overlayFiles)
	if err != nil {
		return result, transactionRuntimeError("overlay_failed", "could not rebuild the prospective repository", err)
	}
	lintResult, err := lint.RunViewAt(ctx, s.Repo, view, s.TxGit, now)
	if err != nil {
		return result, transactionRuntimeError("prospective_lint_failed", "could not re-run prospective lint", err)
	}
	if !lintResult.Valid {
		apiErr := NewError(ExitConflict, "prospective_lint_changed", "the stored transaction no longer produces a valid repository")
		apiErr.Details = map[string]any{"lint": lintResult}
		return result, apiErr
	}
	regeneratedDiff, err := diff.Generate(ctx, s.TxGit, diffChanges)
	if err != nil {
		return result, transactionRuntimeError("diff_failed", "could not regenerate the transaction diff", err)
	}
	if transaction.Digest(regeneratedDiff) != artifacts.Proposal.DiffSHA256 || !bytes.Equal(regeneratedDiff, artifacts.Diff) {
		return result, NewError(ExitConflict, "diff_changed", "the current transaction diff does not match the previewed diff")
	}

	recoveryStore, err := recovery.NewStore(s.Repo)
	if err != nil {
		return result, transactionRuntimeError("recovery_store_failed", "could not open the recovery store", err)
	}
	journal, err := recovery.NewJournal(
		artifacts.Proposal, artifacts.PreviewDigest, originals, originalExists, now, "commit",
	)
	if err != nil {
		return result, transactionRuntimeError("recovery_journal_failed", "could not construct the recovery journal", err)
	}
	if err := recoveryStore.Create(journal, originals); err != nil {
		return result, transactionRuntimeError("recovery_journal_failed", "could not persist the recovery journal", err)
	}
	applyingState := artifacts.State
	applyingState.Status = transaction.StatusApplying
	applyingState.UpdatedAt = now.Format(time.RFC3339Nano)
	if err := store.UpdateState(options.TransactionID, applyingState); err != nil {
		_ = recoveryStore.RemoveForRollback()
		return result, transactionRuntimeError("transaction_state_failed", "could not mark the transaction as applying", err)
	}
	artifacts.State = applyingState
	journal.Phase = recovery.PhaseApplyingFiles
	if err := recoveryStore.Update(journal); err != nil {
		return s.failBeforeCanonicalMutation(store, recoveryStore, artifacts, err)
	}

	for index, operation := range artifacts.Proposal.Operations {
		if err := s.Repo.AtomicApply(
			operation.Path,
			artifacts.Contents[index],
			originals[index],
			originalExists[index],
		); err != nil {
			return s.rollbackCommitFailure(ctx, store, recoveryStore, artifacts, journal, originals, "file_apply_failed", "could not apply all transaction files", err)
		}
		if s.TxHooks != nil {
			if err := s.TxHooks.AfterFileRename(index, operation.Path); err != nil {
				return CommitResult{}, transactionRuntimeError("injected_interruption", "transaction interrupted after a file rename; run lore recover", err)
			}
		}
		journal.Files[index].Applied = true
		if err := recoveryStore.Update(journal); err != nil {
			return s.markRecoveryRequired(store, artifacts, "recovery_journal_failed", "a file was applied but its recovery journal could not be updated", err)
		}
	}
	journal.Phase = recovery.PhaseFilesApplied
	if err := recoveryStore.Update(journal); err != nil {
		return s.markRecoveryRequired(store, artifacts, "recovery_journal_failed", "transaction files were applied but the recovery journal could not be updated", err)
	}
	realLint, err := lint.RunAt(ctx, s.Repo, s.TxGit, s.Clock.Now().UTC())
	if err != nil {
		return s.rollbackCommitFailure(ctx, store, recoveryStore, artifacts, journal, originals, "lint_failed", "could not lint the applied repository", err)
	}
	if !realLint.Valid {
		return s.rollbackCommitFailure(ctx, store, recoveryStore, artifacts, journal, originals, "lint_invalid", "applied transaction failed repository lint", fmt.Errorf("lint reported %d errors", realLint.Errors))
	}

	commitHash, err := s.TxGit.CommitPaths(ctx, s.Repo.Root, artifacts.Proposal.ChangedPaths, artifacts.Proposal.Message)
	if err != nil {
		_ = s.TxGit.ResetPaths(ctx, s.Repo.Root, artifacts.Proposal.ChangedPaths)
		return s.rollbackCommitFailure(ctx, store, recoveryStore, artifacts, journal, originals, "git_commit_failed", "could not create the transaction Git commit", err)
	}
	if s.TxHooks != nil {
		if err := s.TxHooks.AfterGitCommit(commitHash); err != nil {
			return CommitResult{}, transactionRuntimeError("injected_interruption", "transaction interrupted after the Git commit; run lore recover --finalize", err)
		}
	}
	journal.Commit = commitHash
	journal.Phase = recovery.PhaseGitCommitted
	if err := recoveryStore.Update(journal); err != nil {
		return s.markRecoveryRequired(store, artifacts, "recovery_journal_failed", "the Git commit succeeded but the recovery journal could not be updated", err)
	}
	if apiErr := s.verifyCreatedCommit(ctx, artifacts, commitHash); apiErr != nil {
		return s.markRecoveryRequired(store, artifacts, apiErr.Code, apiErr.Message, apiErr)
	}

	committedAt := s.Clock.Now().UTC().Format(time.RFC3339Nano)
	committedState := artifacts.State
	committedState.Status = transaction.StatusCommitted
	committedState.UpdatedAt = committedAt
	committedState.CommittedAt = committedAt
	committedState.Commit = commitHash
	if err := store.UpdateState(options.TransactionID, committedState); err != nil {
		return result, transactionRuntimeError("transaction_state_failed", "the Git commit succeeded but transaction state could not be finalized; run lore recover --finalize", err)
	}
	artifacts.State = committedState
	result = committedResult(artifacts, false)
	// Files have been applied but HEAD still names the preview base while this
	// lint runs. The active recovery journal, stale index, and uncommitted status
	// of source targets are therefore expected until the exact-path commit. Keep
	// warnings for unrelated dirty sources and other real findings.
	result.Warnings = append(result.Warnings, lintWarningsForCommit(realLint, artifacts.Proposal.ChangedPaths)...)

	journal.Phase = recovery.PhaseFinalized
	if err := recoveryStore.Update(journal); err != nil {
		result.Warnings = append(result.Warnings, "Git commit is safe, but the recovery journal could not be finalized; run lore recover --finalize")
	} else if err := recoveryStore.Remove(); err != nil {
		result.Warnings = append(result.Warnings, "Git commit is safe, but the finalized recovery journal could not be removed; run lore recover --finalize")
	}

	shouldPush := s.Repo.Config.Git.AutoPushTransactions
	if options.Push != nil {
		shouldPush = *options.Push
	}
	if !shouldPush {
		result.Warnings = append(result.Warnings, s.bestEffortIndexRefresh(ctx)...)
		return result, nil
	}
	if err := s.TxGit.PushHead(ctx, s.Repo.Root, s.Repo.Config.Git.Remote); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("transaction committed locally but push to %s failed", s.Repo.Config.Git.Remote))
		committedState.PushError = fmt.Sprintf("push to %s failed", s.Repo.Config.Git.Remote)
		committedState.UpdatedAt = s.Clock.Now().UTC().Format(time.RFC3339Nano)
		if stateErr := store.UpdateState(options.TransactionID, committedState); stateErr != nil {
			result.Warnings = append(result.Warnings, "push failure could not be recorded in transaction state")
		}
		result.Warnings = append(result.Warnings, s.bestEffortIndexRefresh(ctx)...)
		if s.Repo.Config.Git.RequirePush {
			apiErr := NewError(ExitRuntime, "push_required_failed", "transaction is safely committed locally, but the required push failed")
			apiErr.Details = map[string]any{
				"transaction_id": options.TransactionID,
				"commit":         commitHash,
				"remote":         s.Repo.Config.Git.Remote,
				"locally_safe":   true,
			}
			apiErr.Cause = err
			return result, apiErr
		}
		return result, nil
	}
	result.Pushed = true
	committedState.Pushed = true
	committedState.PushError = ""
	committedState.UpdatedAt = s.Clock.Now().UTC().Format(time.RFC3339Nano)
	if err := store.UpdateState(options.TransactionID, committedState); err != nil {
		result.Warnings = append(result.Warnings, "push succeeded but transaction state could not record it")
	}
	result.Warnings = append(result.Warnings, s.bestEffortIndexRefresh(ctx)...)
	return result, nil
}

func (s *Service) verifyCommitBase(ctx context.Context, proposal transaction.Proposal) *APIError {
	branch, detached, err := s.TxGit.BranchState(ctx, s.Repo.Root)
	if err != nil {
		return transactionRuntimeError("git_branch_failed", "could not read the current Git branch", err)
	}
	if detached || branch != proposal.BaseBranch {
		apiErr := NewError(ExitConflict, "base_branch_changed", "the current Git branch does not match the preview base")
		apiErr.Details = map[string]any{"expected": proposal.BaseBranch, "actual": branch, "detached": detached}
		return apiErr
	}
	head, err := s.TxGit.Head(ctx, s.Repo.Root)
	if err != nil {
		return transactionRuntimeError("git_head_failed", "could not read the current Git HEAD", err)
	}
	if head != proposal.BaseCommit {
		apiErr := NewError(ExitConflict, "base_commit_changed", "Git HEAD changed after the transaction preview")
		apiErr.Details = map[string]any{"expected": proposal.BaseCommit, "actual": head}
		return apiErr
	}
	return nil
}

func (s *Service) verifyCommitTargets(artifacts transaction.Artifacts) ([][]byte, []bool, []diff.Change, *APIError) {
	originals := make([][]byte, len(artifacts.Proposal.Operations))
	exists := make([]bool, len(artifacts.Proposal.Operations))
	changes := make([]diff.Change, len(artifacts.Proposal.Operations))
	for index, operation := range artifacts.Proposal.Operations {
		absolute, err := s.Repo.SafeContentPath(operation.Path)
		if err != nil {
			return nil, nil, nil, NewError(ExitConflict, "unsafe_target_path", fmt.Sprintf("transaction target is no longer safe: %s", operation.Path))
		}
		if operation.Op == transaction.OperationCreatePage {
			if _, err := os.Lstat(absolute); err == nil {
				return nil, nil, nil, NewError(ExitConflict, "target_exists", fmt.Sprintf("create_page target now exists: %s", operation.Path))
			} else if !errors.Is(err, fs.ErrNotExist) {
				return nil, nil, nil, transactionRuntimeError("target_inspection_failed", fmt.Sprintf("could not inspect target %s", operation.Path), err)
			}
			changes[index] = diff.Change{Path: operation.Path, Result: artifacts.Contents[index], Created: true}
			continue
		}
		original, apiErr := readRegularTarget(absolute, operation.Path)
		if apiErr != nil {
			return nil, nil, nil, apiErr
		}
		actual := docs.Revision(original)
		if actual != operation.OriginalRevision {
			return nil, nil, nil, revisionConflict(operation.Path, operation.OriginalRevision, actual)
		}
		originals[index] = original
		exists[index] = true
		changes[index] = diff.Change{Path: operation.Path, Original: original, Result: artifacts.Contents[index]}
	}
	return originals, exists, changes, nil
}

func (s *Service) verifyCreatedCommit(ctx context.Context, artifacts transaction.Artifacts, commitHash string) *APIError {
	paths, err := s.TxGit.ChangedPathsInCommit(ctx, s.Repo.Root, commitHash)
	if err != nil {
		return transactionRuntimeError("commit_verification_failed", "could not inspect the created Git commit", err)
	}
	expected := append([]string(nil), artifacts.Proposal.ChangedPaths...)
	sort.Strings(expected)
	if !equalStrings(paths, expected) {
		apiErr := NewError(ExitRuntime, "commit_path_mismatch", "created Git commit does not contain exactly the transaction paths")
		apiErr.Details = map[string]any{"expected_paths": expected, "actual_paths": paths, "commit": commitHash}
		return apiErr
	}
	for index, operation := range artifacts.Proposal.Operations {
		blob, err := s.TxGit.BlobAtCommit(ctx, s.Repo.Root, commitHash, operation.Path)
		if err != nil {
			return transactionRuntimeError("commit_verification_failed", fmt.Sprintf("could not inspect committed path %s", operation.Path), err)
		}
		if !bytes.Equal(blob, artifacts.Contents[index]) {
			return NewError(ExitRuntime, "commit_content_mismatch", fmt.Sprintf("committed bytes do not match the proposal for %s", operation.Path))
		}
	}
	return nil
}

func (s *Service) rollbackCommitFailure(
	ctx context.Context,
	store *transaction.Store,
	recoveryStore *recovery.Store,
	artifacts transaction.Artifacts,
	journal recovery.Journal,
	originals [][]byte,
	code, message string,
	cause error,
) (CommitResult, error) {
	_ = s.TxGit.ResetPaths(ctx, s.Repo.Root, artifacts.Proposal.ChangedPaths)
	if err := s.restoreJournalFiles(journal, originals); err != nil {
		return s.markRecoveryRequired(store, artifacts, "rollback_conflict", "automatic rollback could not safely restore every target; run lore recover", err)
	}
	if _, err := lint.RunAt(ctx, s.Repo, s.TxGit, s.Clock.Now().UTC()); err != nil {
		return s.markRecoveryRequired(store, artifacts, "rollback_lint_failed", "files were restored but rollback lint could not run; run lore recover", err)
	}
	failed := artifacts.State
	failed.Status = transaction.StatusFailed
	failed.UpdatedAt = s.Clock.Now().UTC().Format(time.RFC3339Nano)
	failed.FailureCode = code
	failed.FailureMessage = message
	if err := store.UpdateState(artifacts.Proposal.TransactionID, failed); err != nil {
		return CommitResult{}, transactionRuntimeError("transaction_state_failed", "files were rolled back but transaction state could not be updated; run lore recover --rollback", err)
	}
	if err := recoveryStore.RemoveForRollback(); err != nil {
		return CommitResult{}, transactionRuntimeError("recovery_cleanup_failed", "files were rolled back but the recovery journal could not be removed; run lore recover --rollback", err)
	}
	apiErr := NewError(ExitRuntime, code, message)
	apiErr.Cause = cause
	apiErr.Details = map[string]any{"rolled_back": true, "transaction_id": artifacts.Proposal.TransactionID}
	return CommitResult{}, apiErr
}

func (s *Service) restoreJournalFiles(journal recovery.Journal, originals [][]byte) error {
	for index := len(journal.Files) - 1; index >= 0; index-- {
		file := journal.Files[index]
		absolute, err := s.Repo.SafeContentPath(file.Path)
		if err != nil {
			return fmt.Errorf("restore %s: %w", file.Path, err)
		}
		if !file.OriginalExists {
			info, err := os.Lstat(absolute)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return fmt.Errorf("inspect created path %s: %w", file.Path, err)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return fmt.Errorf("created path %s has an unexpected type", file.Path)
			}
			if !file.Applied {
				return fmt.Errorf("unapplied create path %s unexpectedly exists", file.Path)
			}
			current, err := os.ReadFile(absolute)
			if err != nil {
				return fmt.Errorf("read created path %s: %w", file.Path, err)
			}
			if docs.Revision(current) != file.ResultingRevision {
				return fmt.Errorf("created path %s contains an unexpected external edit", file.Path)
			}
			if err := s.Repo.RemoveExpected(file.Path, current); err != nil {
				return fmt.Errorf("remove created path %s: %w", file.Path, err)
			}
			continue
		}
		current, apiErr := readRegularTarget(absolute, file.Path)
		if apiErr != nil {
			return apiErr
		}
		revision := docs.Revision(current)
		if revision == file.OriginalRevision {
			continue
		}
		if !file.Applied {
			return fmt.Errorf("unapplied update path %s contains an unexpected external edit", file.Path)
		}
		if revision != file.ResultingRevision {
			return fmt.Errorf("updated path %s contains an unexpected external edit", file.Path)
		}
		if err := s.Repo.AtomicApply(file.Path, originals[index], current, true); err != nil {
			return fmt.Errorf("restore updated path %s: %w", file.Path, err)
		}
	}
	return nil
}

func (s *Service) markRecoveryRequired(store *transaction.Store, artifacts transaction.Artifacts, code, message string, cause error) (CommitResult, error) {
	state := artifacts.State
	state.Status = transaction.StatusRecoveryRequired
	state.UpdatedAt = s.Clock.Now().UTC().Format(time.RFC3339Nano)
	state.FailureCode = code
	state.FailureMessage = message
	if err := store.UpdateState(artifacts.Proposal.TransactionID, state); err != nil {
		cause = fmt.Errorf("%v; additionally could not persist recovery-required state: %w", cause, err)
	}
	apiErr := NewError(ExitConflict, code, message)
	apiErr.Cause = cause
	apiErr.Details = map[string]any{
		"transaction_id": artifacts.Proposal.TransactionID,
		"recovery":       "run lore recover",
	}
	return CommitResult{}, apiErr
}

func (s *Service) failBeforeCanonicalMutation(store *transaction.Store, recoveryStore *recovery.Store, artifacts transaction.Artifacts, cause error) (CommitResult, error) {
	failed := artifacts.State
	failed.Status = transaction.StatusFailed
	failed.UpdatedAt = s.Clock.Now().UTC().Format(time.RFC3339Nano)
	failed.FailureCode = "recovery_journal_failed"
	failed.FailureMessage = "recovery journal could not enter the applying phase"
	stateErr := store.UpdateState(artifacts.Proposal.TransactionID, failed)
	removeErr := recoveryStore.RemoveForRollback()
	if stateErr != nil || removeErr != nil {
		return s.markRecoveryRequired(store, artifacts, "recovery_journal_failed", "recovery journal failed before file application and could not be cleaned up", cause)
	}
	return CommitResult{}, transactionRuntimeError("recovery_journal_failed", "recovery journal failed before file application", cause)
}

func committedResult(artifacts transaction.Artifacts, already bool) CommitResult {
	warnings := []string{}
	if artifacts.State.PushError != "" {
		warnings = append(warnings, artifacts.State.PushError)
	}
	return CommitResult{
		SchemaVersion:    SchemaVersion,
		TransactionID:    artifacts.Proposal.TransactionID,
		Status:           transaction.StatusCommitted,
		PreviewDigest:    artifacts.PreviewDigest,
		Commit:           artifacts.State.Commit,
		ChangedPaths:     append([]string(nil), artifacts.Proposal.ChangedPaths...),
		CommittedAt:      artifacts.State.CommittedAt,
		Pushed:           artifacts.State.Pushed,
		AlreadyCommitted: already,
		Warnings:         warnings,
	}
}

func digestConflict(transactionID string) *APIError {
	apiErr := NewError(ExitConflict, "preview_digest_mismatch", "preview digest does not match the stored transaction")
	apiErr.Details = map[string]any{"transaction_id": transactionID}
	return apiErr
}

func (s *Service) validateCommitActor(actor string) *APIError {
	if actor != s.transactionActor() {
		apiErr := NewError(ExitConflict, "actor_mismatch", "the current actor is not permitted to commit this transaction")
		apiErr.Details = map[string]any{}
		return apiErr
	}
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
