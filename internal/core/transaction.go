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
	ArtifactState string             `json:"artifact_state"`
}

type TransactionListResult struct {
	SchemaVersion int                  `json:"schema_version"`
	Transactions  []TransactionSummary `json:"transactions"`
}

type TransactionShowResult struct {
	SchemaVersion int                           `json:"schema_version"`
	Proposal      transaction.Proposal          `json:"proposal"`
	State         transaction.State             `json:"state"`
	PreviewDigest string                        `json:"preview_digest"`
	Lint          lint.Result                   `json:"lint"`
	Diff          string                        `json:"diff,omitempty"`
	Retention     *transaction.RetentionReceipt `json:"retention,omitempty"`
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
	handle, apiErr := s.acquireWriteLock(ctx, "preview", now)
	if apiErr != nil {
		return result, apiErr
	}
	defer func() {
		if releaseErr := handle.Release(); releaseErr != nil && returnErr == nil {
			apiErr := NewError(ExitRuntime, "lock_release_failed", "preview completed but the repository write lock could not be released")
			apiErr.Details = map[string]any{"lock_path": lock.Path(s.Repo.Root)}
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
	deletedPaths := make([]string, 0)
	effective := make([]transaction.EffectiveOperation, 0, len(operations))
	diffChanges := make([]diff.Change, 0, len(operations))
	totalResultingContent := 0
	for index, operation := range operations {
		effectiveOperation, original, resulting, created, operationErr := s.effectiveOperation(operation, now)
		if operationErr != nil {
			return result, operationErr
		}
		if !created && !effectiveOperation.Deleted && bytes.Equal(original, resulting) {
			return result, NewError(ExitValidation, "operation_has_no_effect", fmt.Sprintf("transaction operation does not change %s", operation.Path))
		}
		if effectiveOperation.Deleted {
			deletedPaths = append(deletedPaths, operation.Path)
		} else {
			effectiveOperation.ContentFile = fmt.Sprintf("content/%03d.md", index)
			overlayFiles[operation.Path] = resulting
		}
		totalResultingContent += len(resulting)
		if totalResultingContent > transaction.MaxTotalNewContent {
			return result, NewError(ExitValidation, "transaction_content_too_large", fmt.Sprintf("total resulting content exceeds %d bytes", transaction.MaxTotalNewContent))
		}
		effective = append(effective, effectiveOperation)
		diffChanges = append(diffChanges, diff.Change{
			Path: operation.Path, Original: original, Result: resulting, Created: created, Deleted: effectiveOperation.Deleted,
		})
	}

	view, err := repository.NewOverlayViewWithDeletions(s.Repo, nil, overlayFiles, deletedPaths)
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
		if operation.Deleted {
			continue
		}
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
		if apiErr := validatePageUpdateTransition(operation.Op, operation.Path, original, resulting, now); apiErr != nil {
			return transaction.EffectiveOperation{}, nil, nil, false, apiErr
		}
		return transaction.EffectiveOperation{
			Op:                     operation.Op,
			Path:                   operation.Path,
			ExpectedRevision:       operation.ExpectedRevision,
			OriginalRevision:       currentRevision,
			ResultingContentSHA256: docs.SHA256(resulting),
		}, original, resulting, false, nil
	case transaction.OperationPatchPage:
		original, apiErr := readRegularTarget(absolute, operation.Path)
		if apiErr != nil {
			return transaction.EffectiveOperation{}, nil, nil, false, apiErr
		}
		currentRevision := docs.Revision(original)
		if currentRevision != operation.ExpectedRevision {
			return transaction.EffectiveOperation{}, nil, nil, false, revisionConflict(operation.Path, operation.ExpectedRevision, currentRevision)
		}
		resulting, apiErr := applyPageReplacements(operation.Path, original, operation.Replacements)
		if apiErr != nil {
			return transaction.EffectiveOperation{}, nil, nil, false, apiErr
		}
		if int64(len(resulting)) > s.Repo.Config.Capture.MaxBytes {
			return transaction.EffectiveOperation{}, nil, nil, false, NewError(ExitValidation, "page_too_large", fmt.Sprintf("patched page exceeds configured maximum of %d bytes", s.Repo.Config.Capture.MaxBytes))
		}
		if apiErr := validatePageUpdateTransition(operation.Op, operation.Path, original, resulting, now); apiErr != nil {
			return transaction.EffectiveOperation{}, nil, nil, false, apiErr
		}
		return transaction.EffectiveOperation{
			Op:                     operation.Op,
			Path:                   operation.Path,
			ExpectedRevision:       operation.ExpectedRevision,
			OriginalRevision:       currentRevision,
			ResultingContentSHA256: docs.SHA256(resulting),
		}, original, resulting, false, nil
	case transaction.OperationDeletePage:
		original, apiErr := readRegularTarget(absolute, operation.Path)
		if apiErr != nil {
			return transaction.EffectiveOperation{}, nil, nil, false, apiErr
		}
		currentRevision := docs.Revision(original)
		if currentRevision != operation.ExpectedRevision {
			return transaction.EffectiveOperation{}, nil, nil, false, revisionConflict(operation.Path, operation.ExpectedRevision, currentRevision)
		}
		currentDocument, err := docs.Parse(operation.Path, original)
		if err != nil || currentDocument.Page == nil || len(docs.ValidatePage(currentDocument.Page)) > 0 {
			return transaction.EffectiveOperation{}, nil, nil, false, NewError(ExitValidation, "invalid_current_page", fmt.Sprintf("current page is invalid: %s", operation.Path))
		}
		return transaction.EffectiveOperation{
			Op:               operation.Op,
			Path:             operation.Path,
			ExpectedRevision: operation.ExpectedRevision,
			OriginalRevision: currentRevision,
			Deleted:          true,
			Sensitivity:      currentDocument.Sensitivity(),
		}, original, nil, false, nil
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
	case transaction.OperationSetSourceSensitivity:
		original, apiErr := readRegularTarget(absolute, operation.Path)
		if apiErr != nil {
			return transaction.EffectiveOperation{}, nil, nil, false, apiErr
		}
		currentRevision := docs.Revision(original)
		if currentRevision != operation.ExpectedRevision {
			return transaction.EffectiveOperation{}, nil, nil, false, revisionConflict(operation.Path, operation.ExpectedRevision, currentRevision)
		}
		currentDocument, err := docs.Parse(operation.Path, original)
		if err != nil || currentDocument.Source == nil || len(docs.ValidateSource(currentDocument.Source)) > 0 {
			return transaction.EffectiveOperation{}, nil, nil, false, NewError(ExitValidation, "invalid_source", fmt.Sprintf("source sensitivity target is invalid: %s", operation.Path))
		}
		if currentDocument.Source.Sensitivity == operation.Sensitivity {
			return transaction.EffectiveOperation{}, nil, nil, false, NewError(ExitValidation, "operation_has_no_effect", fmt.Sprintf("source already has sensitivity %s: %s", operation.Sensitivity, operation.Path))
		}
		downgrade := sensitivityRank(operation.Sensitivity) < sensitivityRank(currentDocument.Source.Sensitivity)
		if downgrade && !operation.AllowDowngrade {
			return transaction.EffectiveOperation{}, nil, nil, false, NewError(ExitValidation, "sensitivity_downgrade_requires_confirmation", "set_source_sensitivity requires allow_downgrade for a less restrictive classification")
		}
		resulting, err := docs.SetSourceSensitivity(operation.Path, original, operation.Sensitivity)
		if err != nil {
			apiErr := NewError(ExitValidation, "invalid_source", fmt.Sprintf("source sensitivity target is invalid: %s", operation.Path))
			apiErr.Cause = err
			return transaction.EffectiveOperation{}, nil, nil, false, apiErr
		}
		return transaction.EffectiveOperation{
			Op:                     operation.Op,
			Path:                   operation.Path,
			ExpectedRevision:       operation.ExpectedRevision,
			Sensitivity:            operation.Sensitivity,
			AllowDowngrade:         downgrade,
			OriginalRevision:       currentRevision,
			ResultingContentSHA256: docs.SHA256(resulting),
		}, original, resulting, false, nil
	default:
		return transaction.EffectiveOperation{}, nil, nil, false, NewError(ExitValidation, "invalid_operation", "unsupported transaction operation")
	}
}

type pageReplacementMatch struct {
	index int
	start int
	end   int
	new   []byte
}

func applyPageReplacements(path string, original []byte, replacements []transaction.Replacement) ([]byte, *APIError) {
	matches := make([]pageReplacementMatch, 0, len(replacements))
	resultingBytes := len(original)
	for index, replacement := range replacements {
		oldBytes := []byte(replacement.Old)
		start, count := uniqueMatch(original, oldBytes)
		switch {
		case count == 0:
			apiErr := NewError(ExitValidation, "patch_text_not_found", fmt.Sprintf("patch_page replacement %d does not match the current page", index))
			apiErr.Details = map[string]any{"field": fmt.Sprintf("operations[].replacements[%d].old", index), "path": path, "replacement_index": index}
			return nil, apiErr
		case count > 1:
			apiErr := NewError(ExitValidation, "patch_text_ambiguous", fmt.Sprintf("patch_page replacement %d matches the current page more than once", index))
			apiErr.Details = map[string]any{"field": fmt.Sprintf("operations[].replacements[%d].old", index), "path": path, "replacement_index": index}
			return nil, apiErr
		}
		newBytes := []byte(replacement.New)
		resultingBytes += len(newBytes) - len(oldBytes)
		matches = append(matches, pageReplacementMatch{index: index, start: start, end: start + len(oldBytes), new: newBytes})
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].start < matches[j].start })
	for index := 1; index < len(matches); index++ {
		if matches[index].start < matches[index-1].end {
			left, right := matches[index-1].index, matches[index].index
			apiErr := NewError(ExitValidation, "patch_replacements_overlap", "patch_page replacements match overlapping current text")
			apiErr.Details = map[string]any{"path": path, "replacement_indexes": []int{left, right}}
			return nil, apiErr
		}
	}
	resulting := make([]byte, 0, resultingBytes)
	cursor := 0
	for _, match := range matches {
		resulting = append(resulting, original[cursor:match.start]...)
		resulting = append(resulting, match.new...)
		cursor = match.end
	}
	resulting = append(resulting, original[cursor:]...)
	return resulting, nil
}

func uniqueMatch(data, target []byte) (start, count int) {
	start = -1
	for offset := 0; offset <= len(data)-len(target); {
		relative := bytes.Index(data[offset:], target)
		if relative < 0 {
			break
		}
		match := offset + relative
		if count == 0 {
			start = match
		}
		count++
		if count > 1 {
			return start, count
		}
		offset = match + 1
	}
	return start, count
}

func validatePageUpdateTransition(operation transaction.OperationKind, path string, original, resulting []byte, now time.Time) *APIError {
	currentDocument, err := docs.Parse(path, original)
	if err != nil || currentDocument.Page == nil {
		return NewError(ExitValidation, "invalid_current_page", fmt.Sprintf("current page is invalid: %s", path))
	}
	proposedDocument, apiErr := validatePageContent(path, resulting)
	if apiErr != nil {
		return apiErr
	}
	if currentDocument.Page.Created != proposedDocument.Page.Created {
		return NewError(ExitValidation, "immutable_page_created", fmt.Sprintf("%s cannot change created for %s", operation, path))
	}
	currentUpdated, _ := time.Parse("2006-01-02", string(currentDocument.Page.Updated))
	proposedUpdated, _ := time.Parse("2006-01-02", string(proposedDocument.Page.Updated))
	if proposedUpdated.Before(currentUpdated) {
		return NewError(ExitValidation, "updated_regressed", fmt.Sprintf("%s updated date precedes the current value for %s", operation, path))
	}
	today, _ := time.Parse("2006-01-02", now.UTC().Format("2006-01-02"))
	if docs.PageChangedExceptUpdated(currentDocument, proposedDocument) && proposedUpdated.Before(today) {
		minimum := today.Format("2006-01-02")
		apiErr := NewError(ExitValidation, "updated_too_old", fmt.Sprintf("%s updated date must be at least %s for content changes to %s", operation, minimum, path))
		apiErr.Details = map[string]any{
			"field":   "updated",
			"minimum": minimum,
			"path":    path,
		}
		return apiErr
	}
	return nil
}

func sensitivityRank(value string) int {
	switch value {
	case "normal":
		return 0
	case "sensitive":
		return 1
	case "local-only":
		return 2
	default:
		return -1
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
	if artifacts.State.Status == transaction.StatusDiscarded || artifacts.Retention != nil {
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
		Retention:     artifacts.Retention,
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
	deletedPaths := make([]string, 0)
	for index, operation := range artifacts.Proposal.Operations {
		if operation.Deleted {
			deletedPaths = append(deletedPaths, operation.Path)
			continue
		}
		overlayFiles[operation.Path] = artifacts.Contents[index]
	}
	view, viewErr := repository.NewOverlayViewWithDeletions(s.Repo, nil, overlayFiles, deletedPaths)
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

func (s *Service) TransactionDiscard(ctx context.Context, transactionID string) (result TransactionDiscardResult, returnErr error) {
	return s.transactionDiscard(ctx, transactionID, nil)
}

func (s *Service) TransactionDiscardOwned(ctx context.Context, transactionID string, access search.AccessPolicy) (result TransactionDiscardResult, returnErr error) {
	if access.AllowedSensitivities == nil {
		return result, NewError(ExitUsage, "access_policy_required", "transaction discard requires an explicit sensitivity access policy")
	}
	return s.transactionDiscard(ctx, transactionID, &access)
}

func (s *Service) transactionDiscard(ctx context.Context, transactionID string, access *search.AccessPolicy) (result TransactionDiscardResult, returnErr error) {
	now := s.Clock.Now().UTC()
	handle, apiErr := s.acquireWriteLock(ctx, "transaction discard", now)
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
	return NewError(ExitValidation, "transaction_unavailable", "no transaction with this ID is available to the current actor")
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

func (s *Service) acquireWriteLock(ctx context.Context, command string, now time.Time) (*lock.Handle, *APIError) {
	handle, err := lock.Acquire(ctx, s.Repo.Root, command, now, s.WriteLockWait)
	if err == nil {
		return handle, nil
	}
	var contention *lock.ContentionError
	if errors.As(err, &contention) {
		apiErr := NewError(ExitConflict, "repository_locked", contention.Error())
		apiErr.Details = map[string]any{
			"lock_path": lock.Path(s.Repo.Root),
			"waited_ms": contention.Waited.Milliseconds(),
			"retryable": !contention.LegacyDirectory,
		}
		if contention.MetadataAvailable {
			apiErr.Details["pid"] = contention.Metadata.PID
			apiErr.Details["hostname"] = contention.Metadata.Hostname
			apiErr.Details["command"] = contention.Metadata.Command
			apiErr.Details["started_at"] = contention.Metadata.StartedAt
		}
		if contention.LegacyDirectory {
			apiErr.Details["legacy_lock"] = true
			apiErr.Details["manual_recovery"] = "stop all Lore v0.4 writers, verify the recorded owner has exited, then remove the legacy lock directory and retry"
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

func lintWarningsForCommit(result lint.Result, changedPaths []string) []string {
	changed := make(map[string]struct{}, len(changedPaths))
	for _, path := range changedPaths {
		changed[path] = struct{}{}
	}
	warnings := []string{}
	for _, finding := range result.Findings {
		if finding.Severity != lint.SeverityWarning {
			continue
		}
		if finding.Code == "recovery_active" || finding.Code == "index_stale" {
			continue
		}
		if finding.Code == "uncommitted_source_change" {
			if _, expected := changed[finding.Path]; expected {
				continue
			}
		}
		warnings = append(warnings, fmt.Sprintf("%s: %s: %s", finding.Code, finding.Path, finding.Message))
	}
	return warnings
}

func transactionSummary(artifacts transaction.Artifacts) TransactionSummary {
	artifactState := "retained"
	if artifacts.State.Status == transaction.StatusDiscarded {
		artifactState = "discarded"
	} else if artifacts.Retention != nil {
		artifactState = string(artifacts.Retention.Phase)
	}
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
		ArtifactState: artifactState,
	}
}

func (s *Service) transactionActor() string {
	if s.Actor == "" {
		return transaction.DefaultActor
	}
	return s.Actor
}
