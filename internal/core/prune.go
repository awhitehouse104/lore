package core

import (
	"context"
	"fmt"
	"sort"
	"time"

	"lore/internal/docs"
	"lore/internal/transaction"
)

const (
	DefaultTransactionPruneLimit = 100
	MaximumTransactionPruneLimit = 1000
)

type TransactionPruneOptions struct {
	OlderThan time.Duration
	Limit     int
	DryRun    bool
}

type TransactionPruneItem struct {
	TransactionID    string             `json:"transaction_id"`
	Status           transaction.Status `json:"status"`
	CommittedAt      string             `json:"committed_at"`
	ArtifactState    string             `json:"artifact_state"`
	ReclaimableFiles int                `json:"reclaimable_files"`
	ReclaimableBytes int64              `json:"reclaimable_bytes"`
	RemovedFiles     int                `json:"removed_files"`
	RemovedBytes     int64              `json:"removed_bytes"`
}

type TransactionPruneResult struct {
	SchemaVersion    int                    `json:"schema_version"`
	DryRun           bool                   `json:"dry_run"`
	Cutoff           string                 `json:"cutoff"`
	Eligible         int                    `json:"eligible"`
	Selected         int                    `json:"selected"`
	Remaining        int                    `json:"remaining"`
	AlreadyPruned    int                    `json:"already_pruned"`
	Pruned           int                    `json:"pruned"`
	FilesReclaimable int                    `json:"files_reclaimable"`
	BytesReclaimable int64                  `json:"bytes_reclaimable"`
	FilesRemoved     int                    `json:"files_removed"`
	BytesRemoved     int64                  `json:"bytes_removed"`
	Transactions     []TransactionPruneItem `json:"transactions"`
}

type pruneCandidate struct {
	transactionID string
	committedAt   time.Time
}

func (s *Service) TransactionPrune(
	ctx context.Context,
	options TransactionPruneOptions,
) (result TransactionPruneResult, returnErr error) {
	result = TransactionPruneResult{
		SchemaVersion: SchemaVersion,
		DryRun:        options.DryRun,
		Transactions:  []TransactionPruneItem{},
	}
	if s == nil || s.Repo == nil || s.Clock == nil {
		return result, NewError(ExitRuntime, "service_unavailable", "transaction prune service is not fully configured")
	}
	if options.OlderThan <= 0 {
		return result, NewError(ExitUsage, "invalid_older_than", "transaction prune requires a positive --older-than duration")
	}
	if options.Limit < 1 || options.Limit > MaximumTransactionPruneLimit {
		return result, NewError(
			ExitUsage,
			"invalid_limit",
			fmt.Sprintf("transaction prune limit must be between 1 and %d", MaximumTransactionPruneLimit),
		)
	}
	if apiErr := s.requireTransactionGit(ctx); apiErr != nil {
		return result, apiErr
	}

	now := s.Clock.Now().UTC()
	cutoff := now.Add(-options.OlderThan)
	result.Cutoff = cutoff.Format(time.RFC3339Nano)
	handle, apiErr := s.acquireWriteLock(ctx, "transaction prune", now)
	if apiErr != nil {
		return result, apiErr
	}
	defer func() {
		if releaseErr := handle.Release(); releaseErr != nil && returnErr == nil {
			returnErr = transactionRuntimeError(
				"lock_release_failed",
				"transaction pruning completed but the repository write lock could not be released",
				releaseErr,
			)
		}
	}()
	if apiErr := s.requireNoRecovery(); apiErr != nil {
		return result, apiErr
	}

	store, err := transaction.NewStore(s.Repo)
	if err != nil {
		return result, transactionRuntimeError("transaction_store_failed", "could not open the transaction store", err)
	}
	ids, err := store.ListIDsStrict()
	if err != nil {
		return result, transactionRuntimeError("transaction_prune_scan_failed", "could not safely enumerate transactions", err)
	}
	candidates := make([]pruneCandidate, 0, len(ids))
	for _, transactionID := range ids {
		receipt, err := store.LoadReceipt(transactionID)
		if err != nil {
			return result, transactionRuntimeError(
				"transaction_integrity_failed",
				fmt.Sprintf("transaction %s failed receipt verification", transactionID),
				err,
			)
		}
		if receipt.State.Status != transaction.StatusCommitted {
			continue
		}
		committedAt, _ := time.Parse(time.RFC3339Nano, receipt.State.CommittedAt)
		if committedAt.After(cutoff) {
			continue
		}
		if receipt.Retention != nil && receipt.Retention.Phase == transaction.RetentionPruned {
			if _, err := store.InspectPrune(transactionID); err != nil {
				return result, transactionRuntimeError(
					"transaction_integrity_failed",
					fmt.Sprintf("transaction %s failed pruned-receipt verification", transactionID),
					err,
				)
			}
			result.AlreadyPruned++
			continue
		}
		candidates = append(candidates, pruneCandidate{
			transactionID: transactionID,
			committedAt:   committedAt,
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].committedAt.Equal(candidates[j].committedAt) {
			return candidates[i].transactionID < candidates[j].transactionID
		}
		return candidates[i].committedAt.Before(candidates[j].committedAt)
	})
	result.Eligible = len(candidates)
	selected := candidates
	if len(selected) > options.Limit {
		selected = selected[:options.Limit]
	}
	result.Selected = len(selected)
	result.Remaining = result.Eligible - result.Selected

	for _, candidate := range selected {
		inspection, err := store.InspectPrune(candidate.transactionID)
		if err != nil {
			return result, transactionRuntimeError(
				"transaction_integrity_failed",
				fmt.Sprintf("transaction %s failed prune verification", candidate.transactionID),
				err,
			)
		}
		if apiErr := s.verifyPrunableCommit(ctx, inspection); apiErr != nil {
			return result, apiErr
		}
		result.FilesReclaimable += inspection.RemainingPayloadFiles
		result.BytesReclaimable += inspection.RemainingPayloadBytes
		result.Transactions = append(result.Transactions, pruneItem(inspection))
	}
	if options.DryRun {
		return result, nil
	}

	result.Transactions = result.Transactions[:0]
	for _, candidate := range selected {
		if err := ctx.Err(); err != nil {
			return result, transactionRuntimeError("transaction_prune_canceled", "transaction pruning was canceled", err)
		}
		if apiErr := s.requireNoRecovery(); apiErr != nil {
			return result, apiErr
		}
		receipt, err := store.LoadReceipt(candidate.transactionID)
		if err != nil {
			return result, transactionRuntimeError(
				"transaction_integrity_failed",
				fmt.Sprintf("transaction %s failed prune revalidation", candidate.transactionID),
				err,
			)
		}
		committedAt, parseErr := time.Parse(time.RFC3339Nano, receipt.State.CommittedAt)
		if receipt.State.Status != transaction.StatusCommitted ||
			parseErr != nil ||
			committedAt.After(cutoff) {
			return result, NewError(
				ExitConflict,
				"transaction_prune_changed",
				fmt.Sprintf("transaction %s changed after prune selection", candidate.transactionID),
			)
		}
		inspection, err := store.InspectPrune(candidate.transactionID)
		if err != nil {
			return result, transactionRuntimeError(
				"transaction_integrity_failed",
				fmt.Sprintf("transaction %s failed prune revalidation", candidate.transactionID),
				err,
			)
		}
		if apiErr := s.verifyPrunableCommit(ctx, inspection); apiErr != nil {
			return result, apiErr
		}
		pruned, err := store.Prune(ctx, candidate.transactionID, now.Format(time.RFC3339Nano))
		if err != nil {
			return result, transactionRuntimeError(
				"transaction_prune_failed",
				fmt.Sprintf("could not prune transaction %s", candidate.transactionID),
				err,
			)
		}
		item := pruneItem(inspection)
		item.ArtifactState = string(pruned.Retention.Phase)
		item.RemovedFiles = pruned.FilesRemoved
		item.RemovedBytes = pruned.BytesRemoved
		result.Transactions = append(result.Transactions, item)
		if pruned.AlreadyPruned {
			result.AlreadyPruned++
			continue
		}
		result.Pruned++
		result.FilesRemoved += pruned.FilesRemoved
		result.BytesRemoved += pruned.BytesRemoved
	}
	return result, nil
}

func (s *Service) verifyPrunableCommit(
	ctx context.Context,
	inspection transaction.PruneInspection,
) *APIError {
	reachable, err := s.TxGit.CommitReachable(ctx, s.Repo.Root, inspection.State.Commit)
	if err != nil {
		return transactionRuntimeError("git_history_verification_failed", "could not verify transaction commit reachability", err)
	}
	if !reachable {
		apiErr := NewError(
			ExitConflict,
			"transaction_commit_unreachable",
			fmt.Sprintf("transaction %s commit is not reachable from a local Git ref", inspection.State.TransactionID),
		)
		apiErr.Details = map[string]any{
			"transaction_id": inspection.State.TransactionID,
			"commit":         inspection.State.Commit,
		}
		return apiErr
	}
	paths, err := s.TxGit.ChangedPathsInCommit(ctx, s.Repo.Root, inspection.State.Commit)
	if err != nil {
		return transactionRuntimeError("git_history_verification_failed", "could not inspect transaction commit paths", err)
	}
	if !equalStrings(paths, inspection.Proposal.ChangedPaths) {
		apiErr := NewError(
			ExitConflict,
			"transaction_commit_mismatch",
			fmt.Sprintf("transaction %s commit paths no longer match its receipt", inspection.State.TransactionID),
		)
		apiErr.Details = map[string]any{
			"transaction_id": inspection.State.TransactionID,
			"commit":         inspection.State.Commit,
		}
		return apiErr
	}
	for _, operation := range inspection.Proposal.Operations {
		blob, err := s.TxGit.BlobAtCommit(ctx, s.Repo.Root, inspection.State.Commit, operation.Path)
		if err != nil {
			return transactionRuntimeError(
				"git_history_verification_failed",
				fmt.Sprintf("could not inspect committed path %s", operation.Path),
				err,
			)
		}
		if !transaction.DigestEqual(docs.SHA256(blob), operation.ResultingContentSHA256) {
			apiErr := NewError(
				ExitConflict,
				"transaction_commit_mismatch",
				fmt.Sprintf("transaction %s committed bytes no longer match its receipt", inspection.State.TransactionID),
			)
			apiErr.Details = map[string]any{
				"transaction_id": inspection.State.TransactionID,
				"commit":         inspection.State.Commit,
				"path":           operation.Path,
			}
			return apiErr
		}
	}
	return nil
}

func pruneItem(inspection transaction.PruneInspection) TransactionPruneItem {
	artifactState := "retained"
	if inspection.Retention != nil {
		artifactState = string(inspection.Retention.Phase)
	}
	return TransactionPruneItem{
		TransactionID:    inspection.State.TransactionID,
		Status:           inspection.State.Status,
		CommittedAt:      inspection.State.CommittedAt,
		ArtifactState:    artifactState,
		ReclaimableFiles: inspection.RemainingPayloadFiles,
		ReclaimableBytes: inspection.RemainingPayloadBytes,
	}
}
