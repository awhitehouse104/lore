package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"time"

	"lore/internal/docs"
	"lore/internal/lint"
	"lore/internal/recovery"
	"lore/internal/transaction"
)

type RecoveryStatusResult struct {
	SchemaVersion     int            `json:"schema_version"`
	Active            bool           `json:"active"`
	TransactionID     string         `json:"transaction_id,omitempty"`
	Phase             recovery.Phase `json:"phase,omitempty"`
	StartedAt         string         `json:"started_at,omitempty"`
	BaseCommit        string         `json:"base_commit,omitempty"`
	BaseBranch        string         `json:"base_branch,omitempty"`
	Commit            string         `json:"commit,omitempty"`
	ChangedPaths      []string       `json:"changed_paths"`
	RecommendedAction string         `json:"recommended_action"`
}

type RecoveryResult struct {
	SchemaVersion int                `json:"schema_version"`
	Action        string             `json:"action"`
	TransactionID string             `json:"transaction_id"`
	Status        transaction.Status `json:"status"`
	PreviousPhase recovery.Phase     `json:"previous_phase"`
	Commit        string             `json:"commit,omitempty"`
	Lint          lint.Result        `json:"lint"`
	Warnings      []string           `json:"warnings"`
}

func (s *Service) RecoveryStatus(ctx context.Context) (RecoveryStatusResult, error) {
	result := RecoveryStatusResult{
		SchemaVersion: SchemaVersion,
		ChangedPaths:  []string{},
	}
	store, err := recovery.NewStore(s.Repo)
	if err != nil {
		return result, transactionRuntimeError("recovery_store_failed", "could not open the recovery store", err)
	}
	journal, _, err := store.Load()
	if errors.Is(err, fs.ErrNotExist) {
		result.RecommendedAction = "none"
		return result, nil
	}
	if err != nil {
		return result, transactionRuntimeError("recovery_integrity_failed", "the active recovery journal is malformed", err)
	}
	result.Active = true
	result.TransactionID = journal.TransactionID
	result.Phase = journal.Phase
	result.StartedAt = journal.StartedAt
	result.BaseCommit = journal.BaseCommit
	result.BaseBranch = journal.BaseBranch
	result.Commit = journal.Commit
	result.ChangedPaths = append([]string(nil), journal.ChangedPaths...)
	switch journal.Phase {
	case recovery.PhaseGitCommitted, recovery.PhaseFinalized:
		result.RecommendedAction = "lore recover --finalize"
	case recovery.PhaseFilesApplied:
		transactionStore, err := transaction.NewStore(s.Repo)
		if err != nil {
			return result, transactionRuntimeError("transaction_store_failed", "could not open the transaction store", err)
		}
		artifacts, err := transactionStore.Load(journal.TransactionID)
		if err != nil {
			return result, transactionRuntimeError("transaction_integrity_failed", "the recovery transaction failed integrity verification", err)
		}
		if err := validateJournalProposal(journal, artifacts); err != nil {
			return result, transactionRuntimeError("recovery_integrity_failed", "the recovery journal does not match its transaction", err)
		}
		commit, findErr := s.findRecoveryCommit(ctx, journal)
		if findErr != nil {
			return result, findErr
		}
		if commit != "" {
			result.Commit = commit
			result.RecommendedAction = "lore recover --finalize"
		} else {
			result.RecommendedAction = "lore recover --rollback"
		}
	default:
		result.RecommendedAction = "lore recover --rollback"
	}
	return result, nil
}

func (s *Service) RollbackRecovery(ctx context.Context) (result RecoveryResult, returnErr error) {
	now := s.Clock.Now().UTC()
	handle, apiErr := s.acquireWriteLock(ctx, "recover rollback", now)
	if apiErr != nil {
		return result, apiErr
	}
	defer func() {
		if err := handle.Release(); err != nil && returnErr == nil {
			returnErr = transactionRuntimeError("lock_release_failed", "recovery rollback completed but the repository lock could not be released", err)
		}
	}()
	recoveryStore, err := recovery.NewStore(s.Repo)
	if err != nil {
		return result, transactionRuntimeError("recovery_store_failed", "could not open the recovery store", err)
	}
	journal, originals, err := recoveryStore.Load()
	if errors.Is(err, fs.ErrNotExist) {
		return result, NewError(ExitUsage, "no_active_recovery", "there is no active recovery journal")
	}
	if err != nil {
		return result, transactionRuntimeError("recovery_integrity_failed", "the active recovery journal is malformed", err)
	}
	if journal.Phase == recovery.PhaseGitCommitted || journal.Phase == recovery.PhaseFinalized {
		apiErr := NewError(ExitConflict, "recovery_finalize_required", "the recovery journal records a Git commit and cannot be rolled back")
		apiErr.Details = map[string]any{"recommended_action": "lore recover --finalize", "commit": journal.Commit}
		return result, apiErr
	}
	if journal.Phase == recovery.PhaseFilesApplied {
		commitHash, findErr := s.findRecoveryCommit(ctx, journal)
		if findErr != nil {
			return result, findErr
		}
		if commitHash != "" {
			apiErr := NewError(ExitConflict, "recovery_finalize_required", "Git history contains the exact transaction commit; rollback is not permitted")
			apiErr.Details = map[string]any{"recommended_action": "lore recover --finalize", "commit": commitHash}
			return result, apiErr
		}
	}
	transactionStore, err := transaction.NewStore(s.Repo)
	if err != nil {
		return result, transactionRuntimeError("transaction_store_failed", "could not open the transaction store", err)
	}
	state, err := transactionStore.LoadState(journal.TransactionID)
	if err != nil {
		return result, transactionRuntimeError("transaction_integrity_failed", "could not load recovery transaction state", err)
	}
	if !transaction.DigestEqual(state.PreviewDigest, journal.PreviewDigest) {
		return result, NewError(ExitConflict, "recovery_digest_mismatch", "recovery journal digest does not match transaction state")
	}
	if err := s.preflightRecoveryRollback(journal); err != nil {
		_ = s.setRecoveryRequiredState(transactionStore, state, "rollback_conflict", "rollback would overwrite an unexpected target edit")
		apiErr := NewError(ExitConflict, "rollback_conflict", "rollback would overwrite an unexpected target edit")
		apiErr.Cause = err
		apiErr.Details = map[string]any{"transaction_id": journal.TransactionID, "manual_review": true}
		return result, apiErr
	}
	if err := s.TxGit.ResetPaths(ctx, s.Repo.Root, journal.ChangedPaths); err != nil {
		return result, transactionRuntimeError("git_index_restore_failed", "could not clear transaction paths from the Git index", err)
	}
	if err := s.restoreRecoveryFiles(journal, originals); err != nil {
		_ = s.setRecoveryRequiredState(transactionStore, state, "rollback_failed", "rollback could not restore every target")
		return result, transactionRuntimeError("rollback_failed", "rollback could not restore every target; manual review is required", err)
	}
	lintResult, err := lint.RunAt(ctx, s.Repo, s.TxGit, s.Clock.Now().UTC())
	if err != nil {
		return result, transactionRuntimeError("rollback_lint_failed", "files were restored but repository lint could not run", err)
	}
	state.Status = transaction.StatusFailed
	state.UpdatedAt = s.Clock.Now().UTC().Format(time.RFC3339Nano)
	state.FailureCode = "recovered_by_rollback"
	state.FailureMessage = "transaction was rolled back from the recovery journal"
	if err := transactionStore.UpdateState(journal.TransactionID, state); err != nil {
		return result, transactionRuntimeError("transaction_state_failed", "files were restored but transaction state could not be updated", err)
	}
	if err := recoveryStore.RemoveForRollback(); err != nil {
		return result, transactionRuntimeError("recovery_cleanup_failed", "files were restored but the recovery journal could not be removed", err)
	}
	lintResult, err = lint.RunAt(ctx, s.Repo, s.TxGit, s.Clock.Now().UTC())
	if err != nil {
		return result, transactionRuntimeError("rollback_lint_failed", "rollback completed but final repository lint could not run", err)
	}
	return RecoveryResult{
		SchemaVersion: SchemaVersion,
		Action:        "rollback",
		TransactionID: journal.TransactionID,
		Status:        transaction.StatusFailed,
		PreviousPhase: journal.Phase,
		Lint:          lintResult,
		Warnings:      lintWarnings(lintResult),
	}, nil
}

func (s *Service) FinalizeRecovery(ctx context.Context) (result RecoveryResult, returnErr error) {
	now := s.Clock.Now().UTC()
	handle, apiErr := s.acquireWriteLock(ctx, "recover finalize", now)
	if apiErr != nil {
		return result, apiErr
	}
	defer func() {
		if err := handle.Release(); err != nil && returnErr == nil {
			returnErr = transactionRuntimeError("lock_release_failed", "recovery finalize completed but the repository lock could not be released", err)
		}
	}()
	recoveryStore, err := recovery.NewStore(s.Repo)
	if err != nil {
		return result, transactionRuntimeError("recovery_store_failed", "could not open the recovery store", err)
	}
	journal, _, err := recoveryStore.Load()
	if errors.Is(err, fs.ErrNotExist) {
		return result, NewError(ExitUsage, "no_active_recovery", "there is no active recovery journal")
	}
	if err != nil {
		return result, transactionRuntimeError("recovery_integrity_failed", "the active recovery journal is malformed", err)
	}
	transactionStore, err := transaction.NewStore(s.Repo)
	if err != nil {
		return result, transactionRuntimeError("transaction_store_failed", "could not open the transaction store", err)
	}
	artifacts, err := transactionStore.Load(journal.TransactionID)
	if err != nil {
		return result, transactionRuntimeError("transaction_integrity_failed", "the recovery transaction failed integrity verification", err)
	}
	if err := validateJournalProposal(journal, artifacts); err != nil {
		return result, transactionRuntimeError("recovery_integrity_failed", "the recovery journal does not match its transaction", err)
	}
	commitHash, apiErr := s.findRecoveryCommit(ctx, journal)
	if apiErr != nil {
		return result, apiErr
	}
	if commitHash == "" {
		apiErr := NewError(ExitConflict, "recovery_commit_not_found", "no Git commit exactly matching the recovery journal was found")
		apiErr.Details = map[string]any{"recommended_action": "lore recover --rollback"}
		return result, apiErr
	}
	commitTime, err := s.TxGit.CommitTime(ctx, s.Repo.Root, commitHash)
	if err != nil {
		return result, transactionRuntimeError("commit_verification_failed", "could not read the recovery commit timestamp", err)
	}
	state := artifacts.State
	if state.Status == transaction.StatusCommitted && state.Commit != commitHash {
		return result, NewError(ExitConflict, "recovery_commit_mismatch", "transaction state records a different Git commit")
	}
	if state.Status != transaction.StatusCommitted {
		state.Status = transaction.StatusCommitted
		state.Commit = commitHash
		state.CommittedAt = commitTime.Format(time.RFC3339Nano)
		state.UpdatedAt = now.Format(time.RFC3339Nano)
		state.FailureCode = ""
		state.FailureMessage = ""
		if err := transactionStore.UpdateState(journal.TransactionID, state); err != nil {
			return result, transactionRuntimeError("transaction_state_failed", "could not reconcile committed transaction state", err)
		}
	}
	previousPhase := journal.Phase
	if journal.Phase != recovery.PhaseFinalized {
		if journal.Phase != recovery.PhaseGitCommitted {
			journal.Commit = commitHash
			journal.Phase = recovery.PhaseGitCommitted
			if err := recoveryStore.Update(journal); err != nil {
				return result, transactionRuntimeError("recovery_journal_failed", "could not record the recovered Git commit", err)
			}
		}
		journal.Commit = commitHash
		journal.Phase = recovery.PhaseFinalized
		if err := recoveryStore.Update(journal); err != nil {
			return result, transactionRuntimeError("recovery_journal_failed", "could not finalize the recovery journal", err)
		}
	}
	if err := recoveryStore.Remove(); err != nil {
		return result, transactionRuntimeError("recovery_cleanup_failed", "transaction state is committed but the recovery journal could not be removed", err)
	}
	lintResult, err := lint.RunAt(ctx, s.Repo, s.TxGit, s.Clock.Now().UTC())
	if err != nil {
		return result, transactionRuntimeError("recovery_lint_failed", "recovery finalized but repository lint could not run", err)
	}
	return RecoveryResult{
		SchemaVersion: SchemaVersion,
		Action:        "finalize",
		TransactionID: journal.TransactionID,
		Status:        transaction.StatusCommitted,
		PreviousPhase: previousPhase,
		Commit:        commitHash,
		Lint:          lintResult,
		Warnings:      lintWarnings(lintResult),
	}, nil
}

func validateJournalProposal(journal recovery.Journal, artifacts transaction.Artifacts) error {
	proposal := artifacts.Proposal
	if proposal.TransactionID != journal.TransactionID ||
		!transaction.DigestEqual(artifacts.PreviewDigest, journal.PreviewDigest) ||
		proposal.BaseCommit != journal.BaseCommit ||
		proposal.BaseBranch != journal.BaseBranch ||
		!equalStrings(proposal.ChangedPaths, journal.ChangedPaths) ||
		len(proposal.Operations) != len(journal.Files) {
		return fmt.Errorf("journal identity or base state differs from proposal")
	}
	for index, operation := range proposal.Operations {
		if operation.Path != journal.Files[index].Path ||
			operation.ResultingContentSHA256 != journal.Files[index].ResultingRevision {
			return fmt.Errorf("journal file %d differs from proposal", index)
		}
	}
	return nil
}

func (s *Service) findRecoveryCommit(ctx context.Context, journal recovery.Journal) (string, *APIError) {
	children, err := s.TxGit.DirectChildren(ctx, s.Repo.Root, journal.BaseCommit)
	if err != nil {
		return "", transactionRuntimeError("commit_verification_failed", "could not inspect commits after the recovery base", err)
	}
	candidates := children
	if journal.Commit != "" {
		candidates = nil
		for _, child := range children {
			if child == journal.Commit {
				candidates = append(candidates, child)
				break
			}
		}
	}
	var matches []string
	for _, candidate := range candidates {
		onBranch, err := s.TxGit.IsAncestor(ctx, s.Repo.Root, candidate, "refs/heads/"+journal.BaseBranch)
		if err != nil {
			return "", transactionRuntimeError("commit_verification_failed", "could not verify the recovery commit branch", err)
		}
		if !onBranch {
			continue
		}
		paths, err := s.TxGit.ChangedPathsInCommit(ctx, s.Repo.Root, candidate)
		if err != nil {
			return "", transactionRuntimeError("commit_verification_failed", "could not inspect a recovery commit candidate", err)
		}
		expected := append([]string(nil), journal.ChangedPaths...)
		sort.Strings(expected)
		if !equalStrings(paths, expected) {
			continue
		}
		exact := true
		for _, file := range journal.Files {
			blob, err := s.TxGit.BlobAtCommit(ctx, s.Repo.Root, candidate, file.Path)
			if err != nil || docs.Revision(blob) != file.ResultingRevision {
				exact = false
				break
			}
		}
		if exact {
			matches = append(matches, candidate)
		}
	}
	if len(matches) > 1 {
		return "", NewError(ExitConflict, "recovery_commit_ambiguous", "multiple Git commits match the recovery journal")
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return "", nil
}

func (s *Service) preflightRecoveryRollback(journal recovery.Journal) error {
	for _, file := range journal.Files {
		absolute, err := s.Repo.SafeContentPath(file.Path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(absolute)
		if !file.OriginalExists && errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect %s: %w", file.Path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%s has an unexpected type", file.Path)
		}
		current, err := os.ReadFile(absolute)
		if err != nil {
			return fmt.Errorf("read %s: %w", file.Path, err)
		}
		revision := docs.Revision(current)
		if file.OriginalExists {
			if revision != file.OriginalRevision && revision != file.ResultingRevision {
				return fmt.Errorf("%s has an unexpected revision", file.Path)
			}
		} else if revision != file.ResultingRevision {
			return fmt.Errorf("%s has an unexpected revision", file.Path)
		}
	}
	return nil
}

func (s *Service) restoreRecoveryFiles(journal recovery.Journal, originals [][]byte) error {
	for index := len(journal.Files) - 1; index >= 0; index-- {
		file := journal.Files[index]
		absolute, err := s.Repo.SafeContentPath(file.Path)
		if err != nil {
			return err
		}
		if !file.OriginalExists {
			if _, err := os.Lstat(absolute); errors.Is(err, fs.ErrNotExist) {
				continue
			}
			current, err := os.ReadFile(absolute)
			if err != nil {
				return err
			}
			if docs.Revision(current) != file.ResultingRevision {
				return fmt.Errorf("%s changed after rollback preflight", file.Path)
			}
			if err := s.Repo.RemoveExpected(file.Path, current); err != nil {
				return err
			}
			continue
		}
		current, err := os.ReadFile(absolute)
		if err != nil {
			return err
		}
		if docs.Revision(current) == file.OriginalRevision {
			continue
		}
		if docs.Revision(current) != file.ResultingRevision {
			return fmt.Errorf("%s changed after rollback preflight", file.Path)
		}
		if err := s.Repo.AtomicApply(file.Path, originals[index], current, true); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) setRecoveryRequiredState(store *transaction.Store, state transaction.State, code, message string) error {
	state.Status = transaction.StatusRecoveryRequired
	state.UpdatedAt = s.Clock.Now().UTC().Format(time.RFC3339Nano)
	state.FailureCode = code
	state.FailureMessage = message
	return store.UpdateState(state.TransactionID, state)
}
