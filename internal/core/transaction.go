package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"time"

	"lore/internal/diff"
	"lore/internal/docs"
	"lore/internal/lint"
	"lore/internal/lock"
	"lore/internal/repository"
	"lore/internal/search"
	"lore/internal/transaction"
)

const (
	DefaultTransactionLimit = 20
	MaximumTransactionLimit = 200
)

type PreviewResult struct {
	SchemaVersion int         `json:"schema_version"`
	TransactionID string      `json:"transaction_id,omitempty"`
	Status        string      `json:"status"`
	CreatedAt     string      `json:"created_at"`
	BaseCommit    string      `json:"base_commit"`
	BaseBranch    string      `json:"base_branch"`
	Actor         string      `json:"actor"`
	PreviewDigest string      `json:"preview_digest,omitempty"`
	ChangedPaths  []string    `json:"changed_paths"`
	Operations    int         `json:"operations"`
	DiffSHA256    string      `json:"diff_sha256"`
	Diff          string      `json:"diff"`
	Lint          lint.Result `json:"lint"`
	Warnings      []string    `json:"warnings"`
}

type TransactionSummary struct {
	TransactionID string             `json:"transaction_id"`
	Status        transaction.Status `json:"status"`
	CreatedAt     string             `json:"created_at"`
	UpdatedAt     string             `json:"updated_at"`
	Actor         string             `json:"actor"`
	Message       string             `json:"message"`
	BaseCommit    string             `json:"base_commit"`
	BaseBranch    string             `json:"base_branch"`
	PreviewDigest string             `json:"preview_digest"`
	ChangedPaths  []string           `json:"changed_paths"`
	Operations    int                `json:"operations"`
	Commit        string             `json:"commit,omitempty"`
}

type TransactionListResult struct {
	SchemaVersion int                  `json:"schema_version"`
	Transactions  []TransactionSummary `json:"transactions"`
}

type TransactionShowResult struct {
	SchemaVersion int                  `json:"schema_version"`
	Proposal      transaction.Proposal `json:"proposal"`
	State         transaction.State    `json:"state"`
	PreviewDigest string               `json:"preview_digest"`
	Lint          lint.Result          `json:"lint"`
	Diff          string               `json:"diff,omitempty"`
}

type TransactionDiscardResult struct {
	SchemaVersion int                `json:"schema_version"`
	TransactionID string             `json:"transaction_id"`
	Status        transaction.Status `json:"status"`
	Discarded     bool               `json:"discarded"`
}

func (s *Service) Preview(ctx context.Context, requestBytes []byte) (result PreviewResult, returnErr error) {
	return s.preview(ctx, requestBytes, nil)
}

func (s *Service) PreviewAuthorized(ctx context.Context, requestBytes []byte, access search.AccessPolicy) (result PreviewResult, returnErr error) {
	if access.AllowedSensitivities == nil {
		return result, NewError(ExitUsage, "access_policy_required", "transaction preview requires an explicit sensitivity access policy")
	}
	return s.preview(ctx, requestBytes, &access)
}

func (s *Service) preview(ctx context.Context, requestBytes []byte, access *search.AccessPolicy) (result PreviewResult, returnErr error) {
	if s == nil || s.Repo == nil || s.Clock == nil || s.TxIDs == nil {
		return result, NewError(ExitRuntime, "service_unavailable", "transaction service is not fully configured")
	}
	request, err := transaction.DecodeRequest(requestBytes, s.Repo.Config.Capture.MaxBytes)
	if err != nil {
		apiErr := NewError(ExitValidation, "invalid_transaction_request", err.Error())
		apiErr.Cause = err
		return result, apiErr
	}
	if apiErr := s.requireTransactionGit(ctx); apiErr != nil {
		return result, apiErr
	}
	if apiErr := s.requireNoRecovery(); apiErr != nil {
		return result, apiErr
	}

	now := s.Clock.Now().UTC()
	handle, apiErr := acquireWriteLock(s.Repo, "preview", now)
	if apiErr != nil {
		return result, apiErr
	}
	defer func() {
		if releaseErr := handle.Release(); releaseErr != nil && returnErr == nil {
			apiErr := NewError(ExitRuntime, "lock_release_failed", "preview completed but the repository write lock could not be released")
			apiErr.Details = map[string]any{"lock_path": lock.ManualRecoveryPath(s.Repo.Root)}
			apiErr.Cause = releaseErr
			returnErr = apiErr
		}
	}()

	if apiErr := s.requireNoRecovery(); apiErr != nil {
		return result, apiErr
	}
	branch, detached, err := s.TxGit.BranchState(ctx, s.Repo.Root)
	if err != nil {
		return result, transactionRuntimeError("git_branch_failed", "could not read the current Git branch", err)
	}
	if detached || branch == "" {
		return result, NewError(ExitConflict, "detached_head", "transaction preview requires a named Git branch")
	}
	head, err := s.TxGit.Head(ctx, s.Repo.Root)
	if err != nil {
		return result, transactionRuntimeError("git_head_failed", "transaction preview requires at least one Git commit", err)
	}

	operations := append([]transaction.Operation(nil), request.Operations...)
	sort.Slice(operations, func(i, j int) bool { return operations[i].Path < operations[j].Path })
	targets := make([]string, len(operations))
	for index, operation := range operations {
		targets[index] = operation.Path
	}
	changes, err := s.TxGit.Changes(ctx, s.Repo.Root, targets)
	if err != nil {
		return result, transactionRuntimeError("git_status_failed", "could not inspect transaction target paths", err)
	}
	if len(changes) > 0 {
		details := make([]map[string]string, 0, len(changes))
		for _, change := range changes {
			details = append(details, map[string]string{"path": change.Path, "status": change.Status})
		}
		apiErr := NewError(ExitConflict, "target_path_dirty", "one or more transaction target paths have staged or unstaged changes")
		apiErr.Details = map[string]any{"changes": details}
		return result, apiErr
	}

	overlayFiles := make(map[string][]byte, len(operations))
	effective := make([]transaction.EffectiveOperation, 0, len(operations))
	diffChanges := make([]diff.Change, 0, len(operations))
	totalResultingContent := 0
	for index, operation := range operations {
		effectiveOperation, original, resulting, created, operationErr := s.effectiveOperation(operation, now)
		if operationErr != nil {
			return result, operationErr
		}
		if !created && bytes.Equal(original, resulting) {
			return result, NewError(ExitValidation, "operation_has_no_effect", fmt.Sprintf("transaction operation does not change %s", operation.Path))
		}
		effectiveOperation.ContentFile = fmt.Sprintf("content/%03d.md", index)
		totalResultingContent += len(resulting)
		if totalResultingContent > transaction.MaxTotalNewContent {
			return result, NewError(ExitValidation, "transaction_content_too_large", fmt.Sprintf("total resulting content exceeds %d bytes", transaction.MaxTotalNewContent))
		}
		effective = append(effective, effectiveOperation)
		overlayFiles[operation.Path] = resulting
		diffChanges = append(diffChanges, diff.Change{
			Path: operation.Path, Original: original, Result: resulting, Created: created,
		})
	}

	view, err := repository.NewOverlayView(s.Repo, nil, overlayFiles)
	if err != nil {
		return result, transactionRuntimeError("overlay_failed", "could not build prospective repository view", err)
	}
	if apiErr := validateIntegratedPageReferences(view, effective); apiErr != nil {
		return result, apiErr
	}
	if access != nil {
		originals := make([][]byte, len(diffChanges))
		resulting := make([][]byte, len(diffChanges))
		for index, change := range diffChanges {
			originals[index] = change.Original
			resulting[index] = change.Result
		}
		if apiErr := s.authorizeTransactionContent(ctx, effective, originals, resulting, *access); apiErr != nil {
			return result, apiErr
		}
	}
	lintResult, err := lint.RunViewAt(ctx, s.Repo, view, s.TxGit, now)
	if err != nil {
		return result, transactionRuntimeError("prospective_lint_failed", "could not lint the prospective repository", err)
	}
	diffBytes, err := diff.Generate(ctx, s.TxGit, diffChanges)
	if err != nil {
		return result, transactionRuntimeError("diff_failed", "could not generate the transaction diff", err)
	}
	if len(diffBytes) > transaction.MaxDiffBytes {
		return result, NewError(ExitValidation, "diff_too_large", fmt.Sprintf("transaction diff exceeds %d bytes", transaction.MaxDiffBytes))
	}
	warnings := lintWarnings(lintResult)
	displayLint := lintResult
	displayWarnings := warnings
	if access != nil {
		authorizedLint, filterErr := filterLintResult(ctx, view, lintResult, *access)
		if filterErr != nil {
			return result, transactionRuntimeError("lint_authorization_failed", "could not filter prospective lint diagnostics", filterErr)
		}
		displayLint = authorizedLint.Result
		displayWarnings = lintWarnings(displayLint)
	}
	result = PreviewResult{
		SchemaVersion: SchemaVersion,
		Status:        "invalid",
		CreatedAt:     now.Format(time.RFC3339Nano),
		BaseCommit:    head,
		BaseBranch:    branch,
		Actor:         s.transactionActor(),
		ChangedPaths:  targets,
		Operations:    len(effective),
		DiffSHA256:    transaction.Digest(diffBytes),
		Diff:          string(diffBytes),
		Lint:          displayLint,
		Warnings:      displayWarnings,
	}
	if !lintResult.Valid {
		apiErr := NewError(ExitValidation, "prospective_lint_invalid", "the prospective repository has lint errors")
		apiErr.Details = map[string]any{"preview": result}
		return result, apiErr
	}

	transactionID, err := s.TxIDs.New(now)
	if err != nil {
		return result, transactionRuntimeError("transaction_id_failed", "could not generate a transaction ID", err)
	}
	if err := transaction.ValidateTransactionID(transactionID); err != nil {
		return result, transactionRuntimeError("transaction_id_failed", "transaction ID generator returned an invalid ID", err)
	}
	lintBytes, err := marshalJSONLine(lintResult)
	if err != nil {
		return result, transactionRuntimeError("lint_serialization_failed", "could not serialize prospective lint", err)
	}
	proposal := transaction.Proposal{
		SchemaVersion: SchemaVersion,
		TransactionID: transactionID,
		CreatedAt:     now.Format(time.RFC3339Nano),
		BaseCommit:    head,
		BaseBranch:    branch,
		Actor:         s.transactionActor(),
		Message:       request.Message,
		Operations:    effective,
		ChangedPaths:  targets,
		DiffSHA256:    transaction.Digest(diffBytes),
		LintSHA256:    transaction.Digest(lintBytes),
	}
	state := transaction.State{
		SchemaVersion: SchemaVersion,
		TransactionID: transactionID,
		Status:        transaction.StatusPreviewed,
		UpdatedAt:     now.Format(time.RFC3339Nano),
		Lint: transaction.LintSummary{
			Valid: lintResult.Valid, Errors: lintResult.Errors, Warnings: lintResult.Warnings,
		},
	}
	contents := make([][]byte, len(effective))
	for index, operation := range effective {
		contents[index] = overlayFiles[operation.Path]
	}
	store, err := transaction.NewStore(s.Repo)
	if err != nil {
		return result, transactionRuntimeError("transaction_store_failed", "could not open the transaction store", err)
	}
	digest, err := store.Save(transaction.Artifacts{
		Proposal: proposal,
		State:    state,
		Diff:     diffBytes,
		Lint:     lintBytes,
		Contents: contents,
	})
	if err != nil {
		return result, transactionRuntimeError("transaction_store_failed", "could not persist the transaction preview", err)
	}
	result.TransactionID = transactionID
	result.Status = string(transaction.StatusPreviewed)
	result.PreviewDigest = digest
	return result, nil
}

func (s *Service) effectiveOperation(operation transaction.Operation, now time.Time) (transaction.EffectiveOperation, []byte, []byte, bool, *APIError) {
	absolute, err := s.Repo.SafeContentPath(operation.Path)
	if err != nil {
		apiErr := NewError(ExitValidation, "unsafe_target_path", fmt.Sprintf("invalid transaction path %s", operation.Path))
		apiErr.Cause = err
		return transaction.EffectiveOperation{}, nil, nil, false, apiErr
	}
	switch operation.Op {
	case transaction.OperationCreatePage:
		if _, err := os.Lstat(absolute); err == nil {
			return transaction.EffectiveOperation{}, nil, nil, false, NewError(ExitConflict, "target_exists", fmt.Sprintf("create_page target already exists: %s", operation.Path))
		} else if !errors.Is(err, fs.ErrNotExist) {
			return transaction.EffectiveOperation{}, nil, nil, false, transactionRuntimeError("target_inspection_failed", fmt.Sprintf("could not inspect target %s", operation.Path), err)
		}
		resulting := []byte(operation.Content)
		if _, apiErr := validatePageContent(operation.Path, resulting); apiErr != nil {
			return transaction.EffectiveOperation{}, nil, nil, false, apiErr
		}
		return transaction.EffectiveOperation{
			Op:                     operation.Op,
			Path:                   operation.Path,
			ResultingContentSHA256: docs.SHA256(resulting),
		}, nil, resulting, true, nil
	case transaction.OperationUpdatePage:
		original, apiErr := readRegularTarget(absolute, operation.Path)
		if apiErr != nil {
			return transaction.EffectiveOperation{}, nil, nil, false, apiErr
		}
		currentRevision := docs.Revision(original)
		if currentRevision != operation.ExpectedRevision {
			return transaction.EffectiveOperation{}, nil, nil, false, revisionConflict(operation.Path, operation.ExpectedRevision, currentRevision)
		}
		resulting := []byte(operation.Content)
		currentDocument, err := docs.Parse(operation.Path, original)
		if err != nil || currentDocument.Page == nil {
			return transaction.EffectiveOperation{}, nil, nil, false, NewError(ExitValidation, "invalid_current_page", fmt.Sprintf("current page is invalid: %s", operation.Path))
		}
		proposedDocument, apiErr := validatePageContent(operation.Path, resulting)
		if apiErr != nil {
			return transaction.EffectiveOperation{}, nil, nil, false, apiErr
		}
		if currentDocument.Page.ID != proposedDocument.Page.ID {
			return transaction.EffectiveOperation{}, nil, nil, false, NewError(ExitValidation, "immutable_page_id", fmt.Sprintf("update_page cannot change page ID for %s", operation.Path))
		}
		if currentDocument.Page.Created != proposedDocument.Page.Created {
			return transaction.EffectiveOperation{}, nil, nil, false, NewError(ExitValidation, "immutable_page_created", fmt.Sprintf("update_page cannot change created for %s", operation.Path))
		}
		currentUpdated, _ := time.Parse("2006-01-02", string(currentDocument.Page.Updated))
		proposedUpdated, _ := time.Parse("2006-01-02", string(proposedDocument.Page.Updated))
		if proposedUpdated.Before(currentUpdated) {
			return transaction.EffectiveOperation{}, nil, nil, false, NewError(ExitValidation, "updated_regressed", fmt.Sprintf("update_page updated date precedes the current value for %s", operation.Path))
		}
		today, _ := time.Parse("2006-01-02", now.UTC().Format("2006-01-02"))
		if docs.PageChangedExceptUpdated(currentDocument, proposedDocument) && proposedUpdated.Before(today) {
			return transaction.EffectiveOperation{}, nil, nil, false, NewError(ExitValidation, "updated_too_old", fmt.Sprintf("update_page updated date must be at least %s for content changes to %s", today.Format("2006-01-02"), operation.Path))
		}
		return transaction.EffectiveOperation{
			Op:                     operation.Op,
			Path:                   operation.Path,
			ExpectedRevision:       operation.ExpectedRevision,
			OriginalRevision:       currentRevision,
			ResultingContentSHA256: docs.SHA256(resulting),
		}, original, resulting, false, nil
	case transaction.OperationMarkSourceIntegrated:
		original, apiErr := readRegularTarget(absolute, operation.Path)
		if apiErr != nil {
			return transaction.EffectiveOperation{}, nil, nil, false, apiErr
		}
		currentRevision := docs.Revision(original)
		if currentRevision != operation.ExpectedRevision {
			return transaction.EffectiveOperation{}, nil, nil, false, revisionConflict(operation.Path, operation.ExpectedRevision, currentRevision)
		}
		resulting, err := docs.MarkSourceIntegrated(operation.Path, original, now, operation.PageIDs)
		if err != nil {
			apiErr := NewError(ExitValidation, "invalid_source", fmt.Sprintf("source integration target is invalid: %s", operation.Path))
			apiErr.Cause = err
			return transaction.EffectiveOperation{}, nil, nil, false, apiErr
		}
		pageIDs := append([]string(nil), operation.PageIDs...)
		sort.Strings(pageIDs)
		return transaction.EffectiveOperation{
			Op:                     operation.Op,
			Path:                   operation.Path,
			ExpectedRevision:       operation.ExpectedRevision,
			PageIDs:                pageIDs,
			OriginalRevision:       currentRevision,
			ResultingContentSHA256: docs.SHA256(resulting),
		}, original, resulting, false, nil
	default:
		return transaction.EffectiveOperation{}, nil, nil, false, NewError(ExitValidation, "invalid_operation", "unsupported transaction operation")
	}
}

func validatePageContent(path string, data []byte) (*docs.Document, *APIError) {
	document, err := docs.Parse(path, data)
	if err != nil || document.Page == nil {
		apiErr := NewError(ExitValidation, "invalid_page", fmt.Sprintf("proposed page is invalid: %s", path))
		apiErr.Cause = err
		return nil, apiErr
	}
	if validationErrors := docs.Validate(document); len(validationErrors) > 0 {
		apiErr := NewError(ExitValidation, "invalid_page", fmt.Sprintf("proposed page metadata is invalid: %s", path))
		apiErr.Cause = validationErrors[0]
		return nil, apiErr
	}
	return document, nil
}

func readRegularTarget(absolute, relative string) ([]byte, *APIError) {
	info, err := os.Lstat(absolute)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, NewError(ExitConflict, "target_missing", fmt.Sprintf("transaction target does not exist: %s", relative))
		}
		return nil, transactionRuntimeError("target_inspection_failed", fmt.Sprintf("could not inspect target %s", relative), err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, NewError(ExitValidation, "invalid_target_type", fmt.Sprintf("transaction target must be a regular non-symlink file: %s", relative))
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return nil, transactionRuntimeError("target_read_failed", fmt.Sprintf("could not read target %s", relative), err)
	}
	return data, nil
}

func validateIntegratedPageReferences(view repository.View, operations []transaction.EffectiveOperation) *APIError {
	required := make(map[string]struct{})
	for _, operation := range operations {
		if operation.Op == transaction.OperationMarkSourceIntegrated {
			for _, pageID := range operation.PageIDs {
				required[pageID] = struct{}{}
			}
		}
	}
	if len(required) == 0 {
		return nil
	}
	paths, _, err := view.ManagedMarkdown()
	if err != nil {
		return transactionRuntimeError("overlay_failed", "could not list prospective pages", err)
	}
	for _, path := range paths {
		if len(path) < len("pages/") || path[:len("pages/")] != "pages/" {
			continue
		}
		data, err := view.ReadFile(path)
		if err != nil {
			return transactionRuntimeError("overlay_failed", "could not read a prospective page", err)
		}
		document, err := docs.Parse(path, data)
		if err == nil && document.Page != nil {
			delete(required, document.Page.ID)
		}
	}
	if len(required) == 0 {
		return nil
	}
	missing := make([]string, 0, len(required))
	for pageID := range required {
		missing = append(missing, pageID)
	}
	sort.Strings(missing)
	apiErr := NewError(ExitValidation, "integrated_page_missing", "mark_source_integrated references page IDs absent from the prospective repository")
	apiErr.Details = map[string]any{"page_ids": missing}
	return apiErr
}

func (s *Service) TransactionList(status transaction.Status, limit int) (TransactionListResult, error) {
	result := TransactionListResult{SchemaVersion: SchemaVersion, Transactions: []TransactionSummary{}}
	if limit < 1 || limit > MaximumTransactionLimit {
		return result, NewError(ExitUsage, "invalid_limit", fmt.Sprintf("transaction limit must be between 1 and %d", MaximumTransactionLimit))
	}
	store, err := transaction.NewStore(s.Repo)
	if err != nil {
		return result, transactionRuntimeError("transaction_store_failed", "could not open the transaction store", err)
	}
	ids, err := store.ListIDs()
	if err != nil {
		return result, transactionRuntimeError("transaction_list_failed", "could not list transactions", err)
	}
	for _, transactionID := range ids {
		artifacts, err := store.Load(transactionID)
		if err != nil {
			return result, transactionRuntimeError("transaction_integrity_failed", fmt.Sprintf("transaction %s failed integrity verification", transactionID), err)
		}
		if status != "" && artifacts.State.Status != status {
			continue
		}
		result.Transactions = append(result.Transactions, transactionSummary(artifacts))
		if len(result.Transactions) == limit {
			break
		}
	}
	return result, nil
}

func (s *Service) TransactionListOwned(ctx context.Context, status transaction.Status, limit int, access search.AccessPolicy) (TransactionListResult, error) {
	result := TransactionListResult{SchemaVersion: SchemaVersion, Transactions: []TransactionSummary{}}
	if access.AllowedSensitivities == nil {
		return result, NewError(ExitUsage, "access_policy_required", "transaction listing requires an explicit sensitivity access policy")
	}
	if limit < 1 || limit > MaximumTransactionLimit {
		return result, NewError(ExitUsage, "invalid_limit", fmt.Sprintf("transaction limit must be between 1 and %d", MaximumTransactionLimit))
	}
	store, err := transaction.NewStore(s.Repo)
	if err != nil {
		return result, transactionRuntimeError("transaction_store_failed", "could not open the transaction store", err)
	}
	ids, err := store.ListIDs()
	if err != nil {
		return result, transactionRuntimeError("transaction_list_failed", "could not list transactions", err)
	}
	for _, transactionID := range ids {
		artifacts, err := store.Load(transactionID)
		if err != nil {
			return result, transactionRuntimeError("transaction_integrity_failed", "a transaction failed integrity verification", err)
		}
		if artifacts.Proposal.Actor != s.transactionActor() ||
			(status != "" && artifacts.State.Status != status) ||
			!s.transactionArtifactsAuthorized(ctx, artifacts, access) {
			continue
		}
		result.Transactions = append(result.Transactions, transactionSummary(artifacts))
		if len(result.Transactions) == limit {
			break
		}
	}
	return result, nil
}

func (s *Service) TransactionShow(transactionID string, includeDiff bool) (TransactionShowResult, error) {
	var result TransactionShowResult
	store, err := transaction.NewStore(s.Repo)
	if err != nil {
		return result, transactionRuntimeError("transaction_store_failed", "could not open the transaction store", err)
	}
	artifacts, err := store.Load(transactionID)
	if err != nil {
		return result, transactionRuntimeError("transaction_integrity_failed", fmt.Sprintf("transaction %s failed integrity verification", transactionID), err)
	}
	var lintResult lint.Result
	if artifacts.State.Status == transaction.StatusDiscarded {
		lintResult = lint.Result{
			SchemaVersion: SchemaVersion,
			Valid:         artifacts.State.Lint.Valid,
			Errors:        artifacts.State.Lint.Errors,
			Warnings:      artifacts.State.Lint.Warnings,
			Findings:      []lint.Finding{},
		}
	} else {
		if err := json.Unmarshal(artifacts.Lint, &lintResult); err != nil {
			return result, transactionRuntimeError("transaction_integrity_failed", "stored lint artifact is invalid", err)
		}
	}
	result = TransactionShowResult{
		SchemaVersion: SchemaVersion,
		Proposal:      artifacts.Proposal,
		State:         artifacts.State,
		PreviewDigest: artifacts.PreviewDigest,
		Lint:          lintResult,
	}
	if includeDiff {
		result.Diff = string(artifacts.Diff)
	}
	return result, nil
}

func (s *Service) TransactionShowOwned(ctx context.Context, transactionID string, includeDiff bool, access search.AccessPolicy) (TransactionShowResult, error) {
	if access.AllowedSensitivities == nil {
		return TransactionShowResult{}, NewError(ExitUsage, "access_policy_required", "transaction inspection requires an explicit sensitivity access policy")
	}
	if err := transaction.ValidateTransactionID(transactionID); err != nil {
		return TransactionShowResult{}, NewError(ExitUsage, "invalid_transaction_id", "transaction ID is invalid")
	}
	result, err := s.TransactionShow(transactionID, includeDiff)
	if err != nil || result.Proposal.Actor != s.transactionActor() {
		return TransactionShowResult{}, transactionNotFound()
	}
	store, storeErr := transaction.NewStore(s.Repo)
	if storeErr != nil {
		return TransactionShowResult{}, transactionRuntimeError("transaction_store_failed", "could not open the transaction store", storeErr)
	}
	artifacts, loadErr := store.Load(transactionID)
	if loadErr != nil || !s.transactionArtifactsAuthorized(ctx, artifacts, access) {
		return TransactionShowResult{}, transactionNotFound()
	}
	overlayFiles := make(map[string][]byte, len(artifacts.Proposal.Operations))
	for index, operation := range artifacts.Proposal.Operations {
		overlayFiles[operation.Path] = artifacts.Contents[index]
	}
	view, viewErr := repository.NewOverlayView(s.Repo, nil, overlayFiles)
	if viewErr != nil {
		return TransactionShowResult{}, transactionRuntimeError("overlay_failed", "could not authorize transaction inspection", viewErr)
	}
	authorizedLint, filterErr := filterLintResult(ctx, view, result.Lint, access)
	if filterErr != nil {
		return TransactionShowResult{}, transactionRuntimeError("lint_authorization_failed", "could not filter transaction lint diagnostics", filterErr)
	}
	result.Lint = authorizedLint.Result
	return result, nil
}

func (s *Service) TransactionDiscard(transactionID string) (result TransactionDiscardResult, returnErr error) {
	return s.transactionDiscard(context.Background(), transactionID, nil)
}

func (s *Service) TransactionDiscardOwned(ctx context.Context, transactionID string, access search.AccessPolicy) (result TransactionDiscardResult, returnErr error) {
	if access.AllowedSensitivities == nil {
		return result, NewError(ExitUsage, "access_policy_required", "transaction discard requires an explicit sensitivity access policy")
	}
	return s.transactionDiscard(ctx, transactionID, &access)
}

func (s *Service) transactionDiscard(ctx context.Context, transactionID string, access *search.AccessPolicy) (result TransactionDiscardResult, returnErr error) {
	now := s.Clock.Now().UTC()
	handle, apiErr := acquireWriteLock(s.Repo, "transaction discard", now)
	if apiErr != nil {
		return result, apiErr
	}
	defer func() {
		if releaseErr := handle.Release(); releaseErr != nil && returnErr == nil {
			returnErr = transactionRuntimeError("lock_release_failed", "transaction was discarded but the repository write lock could not be released", releaseErr)
		}
	}()
	if apiErr := s.requireNoRecovery(); apiErr != nil {
		return result, apiErr
	}
	store, err := transaction.NewStore(s.Repo)
	if err != nil {
		return result, transactionRuntimeError("transaction_store_failed", "could not open the transaction store", err)
	}
	if access != nil {
		artifacts, loadErr := store.Load(transactionID)
		if loadErr != nil || artifacts.Proposal.Actor != s.transactionActor() {
			return result, transactionNotFound()
		}
		if artifacts.State.Status != transaction.StatusDiscarded &&
			!s.transactionArtifactsAuthorized(ctx, artifacts, *access) {
			return result, transactionNotFound()
		}
	}
	state, err := store.Discard(transactionID, now.Format(time.RFC3339Nano))
	if err != nil {
		return result, transactionRuntimeError("transaction_discard_failed", fmt.Sprintf("could not discard transaction %s", transactionID), err)
	}
	return TransactionDiscardResult{
		SchemaVersion: SchemaVersion,
		TransactionID: transactionID,
		Status:        state.Status,
		Discarded:     true,
	}, nil
}

func transactionNotFound() *APIError {
	return NewError(ExitValidation, "reference_not_found", "transaction was not found")
}

func (s *Service) requireTransactionGit(ctx context.Context) *APIError {
	isRepository, err := s.TxGit.IsRepository(ctx, s.Repo.Root)
	if err != nil {
		return transactionRuntimeError("git_repository_check_failed", "could not inspect the Git repository", err)
	}
	if !isRepository {
		return NewError(ExitValidation, "git_repository_required", "transaction commands require the Lore root to be a Git repository")
	}
	return nil
}

func (s *Service) requireNoRecovery() *APIError {
	path, err := s.Repo.SafeRepositoryPath(".lore/recovery/active")
	if err != nil {
		return transactionRuntimeError("recovery_check_failed", "could not resolve the recovery journal", err)
	}
	if _, err := os.Lstat(path); err == nil {
		apiErr := NewError(ExitConflict, "recovery_required", "an active recovery journal blocks repository writes")
		apiErr.Details = map[string]any{"path": ".lore/recovery/active", "action": "run lore recover"}
		return apiErr
	} else if !errors.Is(err, fs.ErrNotExist) {
		return transactionRuntimeError("recovery_check_failed", "could not inspect the recovery journal", err)
	}
	return nil
}

func acquireWriteLock(repo *repository.Repository, command string, now time.Time) (*lock.Handle, *APIError) {
	handle, err := lock.Acquire(repo.Root, command, now)
	if err == nil {
		return handle, nil
	}
	var contention *lock.ContentionError
	if errors.As(err, &contention) {
		apiErr := NewError(ExitConflict, "repository_locked", contention.Error())
		apiErr.Details = map[string]any{
			"lock_path":       lock.ManualRecoveryPath(repo.Root),
			"pid":             contention.Metadata.PID,
			"hostname":        contention.Metadata.Hostname,
			"command":         contention.Metadata.Command,
			"started_at":      contention.Metadata.StartedAt,
			"manual_recovery": "verify that the owning process has exited, then remove the lock directory manually",
		}
		apiErr.Cause = err
		return nil, apiErr
	}
	return nil, transactionRuntimeError("lock_failed", "could not acquire repository write lock", err)
}

func revisionConflict(path, expected, actual string) *APIError {
	apiErr := NewError(ExitConflict, "revision_conflict", fmt.Sprintf("transaction target revision changed: %s", path))
	apiErr.Details = map[string]any{"path": path, "expected_revision": expected, "actual_revision": actual}
	return apiErr
}

func transactionRuntimeError(code, message string, cause error) *APIError {
	apiErr := NewError(ExitRuntime, code, message)
	apiErr.Cause = cause
	return apiErr
}

func marshalJSONLine(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func lintWarnings(result lint.Result) []string {
	return lintWarningsWithout(result)
}

func lintWarningsWithout(result lint.Result, excludedCodes ...string) []string {
	excluded := make(map[string]struct{}, len(excludedCodes))
	for _, code := range excludedCodes {
		excluded[code] = struct{}{}
	}
	warnings := []string{}
	for _, finding := range result.Findings {
		if finding.Severity == lint.SeverityWarning {
			if _, skip := excluded[finding.Code]; skip {
				continue
			}
			warnings = append(warnings, fmt.Sprintf("%s: %s: %s", finding.Code, finding.Path, finding.Message))
		}
	}
	return warnings
}

func transactionSummary(artifacts transaction.Artifacts) TransactionSummary {
	return TransactionSummary{
		TransactionID: artifacts.Proposal.TransactionID,
		Status:        artifacts.State.Status,
		CreatedAt:     artifacts.Proposal.CreatedAt,
		UpdatedAt:     artifacts.State.UpdatedAt,
		Actor:         artifacts.Proposal.Actor,
		Message:       artifacts.Proposal.Message,
		BaseCommit:    artifacts.Proposal.BaseCommit,
		BaseBranch:    artifacts.Proposal.BaseBranch,
		PreviewDigest: artifacts.PreviewDigest,
		ChangedPaths:  append([]string(nil), artifacts.Proposal.ChangedPaths...),
		Operations:    len(artifacts.Proposal.Operations),
		Commit:        artifacts.State.Commit,
	}
}

func (s *Service) transactionActor() string {
	if s.Actor == "" {
		return transaction.DefaultActor
	}
	return s.Actor
}
