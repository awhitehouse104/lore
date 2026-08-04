package core

import (
	"context"
	"os"

	"lore/internal/catalog"
	"lore/internal/docs"
	"lore/internal/search"
	"lore/internal/transaction"
)

func (s *Service) authorizeTransactionContent(
	ctx context.Context,
	operations []transaction.EffectiveOperation,
	originals, resulting [][]byte,
	access search.AccessPolicy,
) *APIError {
	resultingPages := make(map[string]string)
	for index, operation := range operations {
		if len(originals[index]) > 0 {
			document, err := docs.Parse(operation.Path, originals[index])
			if err == nil && !access.Allows(document.Sensitivity()) {
				return authorizationDenied()
			}
		}
		if operation.Deleted {
			continue
		}
		document, err := docs.Parse(operation.Path, resulting[index])
		if err == nil {
			if !access.Allows(document.Sensitivity()) {
				return authorizationDenied()
			}
			if document.Page != nil {
				resultingPages[document.Page.ID] = document.Sensitivity()
			}
		}
	}
	if apiErr := s.authorizeIntegratedPages(ctx, operations, resultingPages, access); apiErr != nil {
		return apiErr
	}
	return nil
}

func (s *Service) authorizeIntegratedPages(
	ctx context.Context,
	operations []transaction.EffectiveOperation,
	resultingPages map[string]string,
	access search.AccessPolicy,
) *APIError {
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
	for pageID := range required {
		if sensitivity, ok := resultingPages[pageID]; ok {
			if !access.Allows(sensitivity) {
				return authorizationDenied()
			}
			delete(required, pageID)
		}
	}
	if len(required) == 0 {
		return nil
	}
	documentCatalog, _, err := catalog.Scan(ctx, s.Repo, false)
	if err != nil {
		apiErr := NewError(ExitRuntime, "catalog_scan_failed", "could not authorize transaction page references")
		apiErr.Cause = err
		return apiErr
	}
	for _, document := range documentCatalog.Documents {
		if document.Page == nil {
			continue
		}
		if _, needed := required[document.Page.ID]; !needed {
			continue
		}
		if !access.Allows(document.Sensitivity()) {
			return authorizationDenied()
		}
		delete(required, document.Page.ID)
	}
	if len(required) > 0 {
		return authorizationDenied()
	}
	return nil
}

func (s *Service) transactionArtifactsAuthorized(ctx context.Context, artifacts transaction.Artifacts, access search.AccessPolicy) bool {
	if len(artifacts.Contents) != len(artifacts.Proposal.Operations) {
		return false
	}
	operations := artifacts.Proposal.Operations
	originals := make([][]byte, len(operations))
	resulting := make([][]byte, len(operations))
	for index, operation := range operations {
		resulting[index] = artifacts.Contents[index]
		if operation.Op == transaction.OperationCreatePage {
			continue
		}
		if operation.Deleted {
			if !access.Allows(operation.Sensitivity) {
				return false
			}
			continue
		}
		absolute, err := s.Repo.SafeContentPath(operation.Path)
		if err != nil {
			return false
		}
		data, err := os.ReadFile(absolute)
		if err != nil {
			return false
		}
		originals[index] = data
	}
	return s.authorizeTransactionContent(ctx, operations, originals, resulting, access) == nil
}

func authorizationDenied() *APIError {
	return NewError(ExitValidation, "permission_denied", "the principal is not authorized for one or more transaction targets")
}
