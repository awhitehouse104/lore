package core

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"lore/internal/catalog"
	"lore/internal/docs"
	"lore/internal/markdownlink"
	"lore/internal/repository"
	"lore/internal/search"
)

type PageReferenceTarget struct {
	Path        string `json:"path"`
	ID          string `json:"id"`
	Title       string `json:"title"`
	Sensitivity string `json:"sensitivity"`
	Revision    string `json:"revision"`
}

type LinkReference struct {
	Path        string `json:"path"`
	ID          string `json:"id"`
	Title       string `json:"title,omitempty"`
	Sensitivity string `json:"sensitivity"`
	Revision    string `json:"revision"`
	Line        int    `json:"line"`
	Destination string `json:"destination"`
}

type SourceIntegrationReference struct {
	Path        string `json:"path"`
	ID          string `json:"id"`
	Sensitivity string `json:"sensitivity"`
	Revision    string `json:"revision"`
	Line        int    `json:"line"`
}

type PageReferencesResult struct {
	SchemaVersion            int                          `json:"schema_version"`
	Target                   PageReferenceTarget          `json:"target"`
	LiveBacklinks            []LinkReference              `json:"live_backlinks"`
	HistoricalSourceMentions []LinkReference              `json:"historical_source_mentions"`
	SourceIntegrations       []SourceIntegrationReference `json:"source_integrations"`
	Warnings                 []catalog.Warning            `json:"warnings"`
}

func (s *Service) PageReferences(ctx context.Context, reference string) (PageReferencesResult, error) {
	return s.pageReferences(ctx, reference, search.AllAccessPolicy())
}

func (s *Service) PageReferencesAuthorized(ctx context.Context, reference string, access search.AccessPolicy) (PageReferencesResult, error) {
	if access.AllowedSensitivities == nil {
		return PageReferencesResult{}, NewError(ExitUsage, "access_policy_required", "page references require an explicit sensitivity access policy")
	}
	return s.pageReferences(ctx, reference, access)
}

func (s *Service) pageReferences(ctx context.Context, reference string, access search.AccessPolicy) (PageReferencesResult, error) {
	result := PageReferencesResult{
		SchemaVersion:            SchemaVersion,
		LiveBacklinks:            []LinkReference{},
		HistoricalSourceMentions: []LinkReference{},
		SourceIntegrations:       []SourceIntegrationReference{},
		Warnings:                 []catalog.Warning{},
	}
	if s == nil || s.Repo == nil {
		return result, NewError(ExitRuntime, "service_unavailable", "page-reference service is not fully configured")
	}
	documentCatalog, warnings, err := catalog.Scan(ctx, s.Repo, false)
	if err != nil {
		apiErr := NewError(ExitRuntime, "catalog_scan_failed", "could not scan managed documents")
		apiErr.Cause = err
		return result, apiErr
	}
	result.Warnings = warnings
	authorized := catalog.Catalog{Documents: make([]*docs.Document, 0, len(documentCatalog.Documents))}
	for _, document := range documentCatalog.Documents {
		if access.Allows(document.Sensitivity()) {
			authorized.Documents = append(authorized.Documents, document)
		}
	}
	resolvedReference, err := normalizeReadReference(reference)
	if err != nil {
		apiErr := NewError(ExitUsage, "unsafe_reference", err.Error())
		apiErr.Cause = err
		return result, apiErr
	}
	target, err := authorized.Resolve(s.Repo, resolvedReference)
	if err != nil {
		return result, referenceResolutionError(reference, err)
	}
	if target.Page == nil {
		return result, NewError(ExitValidation, "page_required", "page references require a synthesized page target")
	}
	result.Target = PageReferenceTarget{
		Path:        target.Path,
		ID:          target.Page.ID,
		Title:       target.Page.Title,
		Sensitivity: target.Sensitivity(),
		Revision:    docs.Revision(target.Data),
	}
	for _, document := range authorized.Documents {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		bodyLine := bytes.Count(document.Data[:document.BodyOffset], []byte{'\n'}) + 1
		for index, line := range strings.Split(string(document.Body), "\n") {
			for _, destination := range markdownlink.Destinations(line) {
				path, local := localLinkTarget(s.Repo, document.Path, destination)
				if !local || path != target.Path {
					continue
				}
				reference := LinkReference{
					Path:        document.Path,
					ID:          document.ID(),
					Title:       document.Title(),
					Sensitivity: document.Sensitivity(),
					Revision:    docs.Revision(document.Data),
					Line:        bodyLine + index,
					Destination: destination,
				}
				if document.Page != nil {
					result.LiveBacklinks = append(result.LiveBacklinks, reference)
				} else {
					result.HistoricalSourceMentions = append(result.HistoricalSourceMentions, reference)
				}
			}
		}
		if document.Source != nil && containsString(document.Source.IntegratedInto, target.Page.ID) {
			result.SourceIntegrations = append(result.SourceIntegrations, SourceIntegrationReference{
				Path:        document.Path,
				ID:          document.Source.ID,
				Sensitivity: document.Sensitivity(),
				Revision:    docs.Revision(document.Data),
				Line:        frontmatterFieldLine(document.Data, "integrated_into"),
			})
		}
	}
	sort.Slice(result.LiveBacklinks, func(i, j int) bool { return linkReferenceLess(result.LiveBacklinks[i], result.LiveBacklinks[j]) })
	sort.Slice(result.HistoricalSourceMentions, func(i, j int) bool {
		return linkReferenceLess(result.HistoricalSourceMentions[i], result.HistoricalSourceMentions[j])
	})
	sort.Slice(result.SourceIntegrations, func(i, j int) bool {
		left, right := result.SourceIntegrations[i], result.SourceIntegrations[j]
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		return left.ID < right.ID
	})
	return result, nil
}

func referenceResolutionError(reference string, err error) *APIError {
	var ambiguous *catalog.AmbiguousError
	var unsafe *catalog.UnsafeReferenceError
	var notFound *catalog.NotFoundError
	switch {
	case errors.As(err, &ambiguous):
		apiErr := NewError(ExitConflict, "ambiguous_reference", "reference matched more than one authorized document")
		apiErr.Details = map[string]any{"reference": reference, "rule": ambiguous.Rule, "candidates": ambiguous.Candidates}
		apiErr.Cause = err
		return apiErr
	case errors.As(err, &unsafe):
		apiErr := NewError(ExitUsage, "unsafe_reference", unsafe.Error())
		apiErr.Cause = err
		return apiErr
	case errors.As(err, &notFound):
		apiErr := NewError(ExitValidation, "reference_not_found", "no authorized managed document matches reference")
		apiErr.Cause = err
		return apiErr
	default:
		apiErr := NewError(ExitRuntime, "reference_resolution_failed", "could not resolve document reference")
		apiErr.Cause = err
		return apiErr
	}
}

func localLinkTarget(repo *repository.Repository, documentPath, destination string) (string, bool) {
	if destination == "" || strings.HasPrefix(destination, "#") {
		return "", false
	}
	parsed, err := url.Parse(destination)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" {
		return "", false
	}
	targetPath, err := url.PathUnescape(parsed.Path)
	if err != nil || targetPath == "" || filepath.IsAbs(filepath.FromSlash(targetPath)) {
		return "", false
	}
	relative := filepath.Clean(filepath.Join(filepath.Dir(filepath.FromSlash(documentPath)), filepath.FromSlash(targetPath)))
	if _, err := repo.SafeRepositoryPath(filepath.ToSlash(relative)); err != nil {
		return "", false
	}
	return filepath.ToSlash(relative), true
}

func linkReferenceLess(left, right LinkReference) bool {
	if left.Path != right.Path {
		return left.Path < right.Path
	}
	if left.Line != right.Line {
		return left.Line < right.Line
	}
	return left.Destination < right.Destination
}

func frontmatterFieldLine(data []byte, field string) int {
	prefix := field + ":"
	for index, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return index + 1
		}
	}
	return 0
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
