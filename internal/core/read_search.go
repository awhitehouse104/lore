package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"lore/internal/catalog"
	"lore/internal/docs"
	"lore/internal/search"
)

type LineRange struct {
	Start int
	End   int
}

type ReadResult struct {
	SchemaVersion int    `json:"schema_version"`
	Path          string `json:"path"`
	URI           string `json:"uri"`
	ID            string `json:"id"`
	Title         string `json:"title"`
	Kind          string `json:"kind"`
	Sensitivity   string `json:"sensitivity"`
	LineStart     int    `json:"line_start"`
	LineEnd       int    `json:"line_end"`
	More          bool   `json:"more"`
	Revision      string `json:"revision"`
	Content       string `json:"content"`
}

const (
	DefaultReadManyLines = 160
	MaximumReadManyItems = 8
	MaximumReadManyLines = 400
	MaximumReadItemBytes = 256 * 1024
	MaximumReadManyBytes = 512 * 1024
)

type ReadManyRequest struct {
	Ref       string `json:"ref"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
}

type ReadManyResult struct {
	SchemaVersion int          `json:"schema_version"`
	Documents     []ReadResult `json:"documents"`
	TotalBytes    int          `json:"total_bytes"`
}

type SearchResult struct {
	SchemaVersion    int                 `json:"schema_version"`
	Query            string              `json:"query"`
	Backend          search.Backend      `json:"backend"`
	BackendRequested search.Backend      `json:"backend_requested"`
	Matching         search.MatchingMode `json:"matching"`
	FuzzyExpanded    bool                `json:"fuzzy_expanded"`
	IndexState       string              `json:"index_state"`
	Results          []search.Result     `json:"results"`
	Warnings         []catalog.Warning   `json:"warnings"`
}

func ParseLineRange(value string) (LineRange, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return LineRange{}, fmt.Errorf("line range must use START:END")
	}
	start, err := strconv.Atoi(parts[0])
	if err != nil || start <= 0 {
		return LineRange{}, fmt.Errorf("line range start must be a positive integer")
	}
	end, err := strconv.Atoi(parts[1])
	if err != nil || end <= 0 {
		return LineRange{}, fmt.Errorf("line range end must be a positive integer")
	}
	if end < start {
		return LineRange{}, fmt.Errorf("line range end must not precede start")
	}
	return LineRange{Start: start, End: end}, nil
}

func (s *Service) Read(ctx context.Context, reference string, requested *LineRange) (ReadResult, error) {
	return s.read(ctx, reference, requested, search.AllAccessPolicy())
}

func (s *Service) ReadAuthorized(ctx context.Context, reference string, requested *LineRange, access search.AccessPolicy) (ReadResult, error) {
	if access.AllowedSensitivities == nil {
		return ReadResult{}, NewError(ExitUsage, "access_policy_required", "read requires an explicit sensitivity access policy")
	}
	return s.read(ctx, reference, requested, access)
}

func (s *Service) read(ctx context.Context, reference string, requested *LineRange, access search.AccessPolicy) (ReadResult, error) {
	if s == nil || s.Repo == nil {
		return ReadResult{}, NewError(ExitRuntime, "service_unavailable", "read service is not fully configured")
	}
	documentCatalog, _, err := catalog.Scan(ctx, s.Repo, false)
	if err != nil {
		apiErr := NewError(ExitRuntime, "catalog_scan_failed", "could not scan managed documents")
		apiErr.Cause = err
		return ReadResult{}, apiErr
	}
	return s.readFromCatalog(documentCatalog, reference, requested, access)
}

func (s *Service) ReadMany(ctx context.Context, requests []ReadManyRequest) (ReadManyResult, error) {
	return s.readMany(ctx, requests, search.AllAccessPolicy())
}

func (s *Service) ReadManyAuthorized(ctx context.Context, requests []ReadManyRequest, access search.AccessPolicy) (ReadManyResult, error) {
	if access.AllowedSensitivities == nil {
		return ReadManyResult{}, NewError(ExitUsage, "access_policy_required", "batch read requires an explicit sensitivity access policy")
	}
	return s.readMany(ctx, requests, access)
}

func (s *Service) readMany(ctx context.Context, requests []ReadManyRequest, access search.AccessPolicy) (ReadManyResult, error) {
	result := ReadManyResult{SchemaVersion: SchemaVersion, Documents: []ReadResult{}}
	if s == nil || s.Repo == nil {
		return result, NewError(ExitRuntime, "service_unavailable", "read service is not fully configured")
	}
	if len(requests) < 1 || len(requests) > MaximumReadManyItems {
		return result, NewError(ExitUsage, "invalid_batch_size", fmt.Sprintf("batch read requires between 1 and %d requests", MaximumReadManyItems))
	}
	documentCatalog, _, err := catalog.Scan(ctx, s.Repo, false)
	if err != nil {
		apiErr := NewError(ExitRuntime, "catalog_scan_failed", "could not scan managed documents")
		apiErr.Cause = err
		return result, apiErr
	}
	result.Documents = make([]ReadResult, 0, len(requests))
	for index, request := range requests {
		start := request.StartLine
		if start == 0 {
			start = 1
		}
		end := request.EndLine
		if end == 0 {
			end = start + DefaultReadManyLines - 1
		}
		if start < 1 || end < start || end-start+1 > MaximumReadManyLines {
			apiErr := NewError(ExitValidation, "invalid_line_range", fmt.Sprintf("batch read request %d must contain between 1 and %d lines", index, MaximumReadManyLines))
			apiErr.Details = map[string]any{"index": index}
			return result, apiErr
		}
		document, err := s.readFromCatalog(documentCatalog, request.Ref, &LineRange{Start: start, End: end}, access)
		if err != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) {
				if apiErr.Details == nil {
					apiErr.Details = map[string]any{}
				}
				apiErr.Details["index"] = index
			}
			return result, err
		}
		contentBytes := len([]byte(document.Content))
		if contentBytes > MaximumReadItemBytes {
			apiErr := NewError(ExitValidation, "read_too_large", fmt.Sprintf("batch read request %d exceeds the %d-byte per-document limit", index, MaximumReadItemBytes))
			apiErr.Details = map[string]any{"index": index}
			return result, apiErr
		}
		if result.TotalBytes+contentBytes > MaximumReadManyBytes {
			apiErr := NewError(ExitValidation, "batch_read_too_large", fmt.Sprintf("batch read exceeds the %d-byte aggregate limit; request fewer documents or smaller ranges", MaximumReadManyBytes))
			apiErr.Details = map[string]any{"index": index, "bytes_before": result.TotalBytes}
			return result, apiErr
		}
		result.Documents = append(result.Documents, document)
		result.TotalBytes += contentBytes
	}
	return result, nil
}

func (s *Service) readFromCatalog(documentCatalog catalog.Catalog, reference string, requested *LineRange, access search.AccessPolicy) (ReadResult, error) {
	resolvedReference, err := normalizeReadReference(reference)
	if err != nil {
		apiErr := NewError(ExitUsage, "unsafe_reference", err.Error())
		apiErr.Cause = err
		return ReadResult{}, apiErr
	}
	authorizedCatalog := catalog.Catalog{Documents: make([]*docs.Document, 0, len(documentCatalog.Documents))}
	for _, candidate := range documentCatalog.Documents {
		if access.Allows(candidate.Sensitivity()) {
			authorizedCatalog.Documents = append(authorizedCatalog.Documents, candidate)
		}
	}
	document, err := authorizedCatalog.Resolve(s.Repo, resolvedReference)
	if err != nil {
		var ambiguous *catalog.AmbiguousError
		var unsafe *catalog.UnsafeReferenceError
		var notFound *catalog.NotFoundError
		switch {
		case errors.As(err, &ambiguous):
			return ReadResult{}, &APIError{
				Code:     "ambiguous_reference",
				Message:  "reference matched more than one document",
				Details:  map[string]any{"reference": reference, "rule": ambiguous.Rule, "candidates": ambiguous.Candidates},
				ExitCode: ExitConflict,
				Cause:    err,
			}
		case errors.As(err, &unsafe):
			apiErr := NewError(ExitUsage, "unsafe_reference", unsafe.Error())
			apiErr.Cause = err
			return ReadResult{}, apiErr
		case errors.As(err, &notFound):
			apiErr := NewError(ExitValidation, "reference_not_found", notFound.Error())
			apiErr.Cause = err
			return ReadResult{}, apiErr
		default:
			apiErr := NewError(ExitRuntime, "reference_resolution_failed", "could not resolve document reference")
			apiErr.Cause = err
			return ReadResult{}, apiErr
		}
	}
	content, lineStart, lineEnd, err := sliceLines(document.Data, requested)
	if err != nil {
		apiErr := NewError(ExitValidation, "invalid_line_range", err.Error())
		apiErr.Cause = err
		return ReadResult{}, apiErr
	}
	return ReadResult{
		SchemaVersion: SchemaVersion,
		Path:          document.Path,
		URI:           fmt.Sprintf("lore://%s#L%d-L%d", document.Path, lineStart, lineEnd),
		ID:            document.ID(),
		Title:         document.Title(),
		Kind:          document.Kind(),
		Sensitivity:   document.Sensitivity(),
		LineStart:     lineStart,
		LineEnd:       lineEnd,
		More:          lineEnd < lineCount(document.Data),
		Revision:      docs.Revision(document.Data),
		Content:       string(content),
	}, nil
}

func (s *Service) Search(ctx context.Context, query search.Query) (SearchResult, error) {
	if s == nil || s.Repo == nil || s.Searcher == nil {
		return SearchResult{}, NewError(ExitRuntime, "service_unavailable", "search service is not fully configured")
	}
	if query.Kind != "" && !docs.ValidToken(query.Kind) {
		return SearchResult{}, NewError(ExitValidation, "invalid_kind", "kind must match ^[a-z][a-z0-9_-]*$")
	}
	if query.Access.AllowedSensitivities == nil {
		return SearchResult{}, NewError(ExitUsage, "access_policy_required", "search requires an explicit sensitivity access policy")
	}
	for _, path := range query.Paths {
		if _, err := s.Repo.SafeContentPath(strings.TrimSuffix(path, "/")); err != nil {
			apiErr := NewError(ExitValidation, "invalid_search_path", "search path filter is not a safe managed path")
			apiErr.Cause = err
			return SearchResult{}, apiErr
		}
	}
	if err := search.ValidateQuery(query); err != nil {
		var matchingErr *search.MatchingError
		if errors.As(err, &matchingErr) {
			apiErr := NewError(ExitUsage, matchingErr.Code, matchingErr.Message)
			apiErr.Cause = matchingErr
			return SearchResult{}, apiErr
		}
		apiErr := NewError(ExitValidation, "invalid_search", err.Error())
		apiErr.Cause = err
		return SearchResult{}, apiErr
	}
	var detailed search.DetailedResponse
	var err error
	if searcher, ok := s.Searcher.(search.DetailedSearcher); ok {
		detailed, err = searcher.SearchDetailed(ctx, s.Repo, query)
	} else {
		detailed.Results, detailed.Warnings, err = s.Searcher.Search(ctx, s.Repo, query)
		detailed.Backend = search.BackendFilesystem
		detailed.BackendRequested = query.Backend
		if detailed.BackendRequested == "" {
			detailed.BackendRequested = search.BackendAuto
		}
		detailed.Matching = search.NormalizeMatching(query.Matching)
		for _, result := range detailed.Results {
			if len(result.FuzzyMatches) > 0 {
				detailed.FuzzyExpanded = true
				break
			}
		}
	}
	if err != nil {
		var matchingErr *search.MatchingError
		if errors.As(err, &matchingErr) {
			exitCode := ExitRuntime
			if matchingErr.Code == "fuzzy_query_too_broad" {
				exitCode = ExitUsage
			}
			apiErr := NewError(exitCode, matchingErr.Code, matchingErr.Message)
			apiErr.Cause = matchingErr
			return SearchResult{}, apiErr
		}
		var backendErr *search.BackendError
		if errors.As(err, &backendErr) {
			exitCode := ExitRuntime
			switch backendErr.Kind {
			case search.BackendErrorUsage:
				exitCode = ExitUsage
			case search.BackendErrorConflict:
				exitCode = ExitConflict
			}
			apiErr := NewError(exitCode, backendErr.Code, backendErr.Message)
			apiErr.Details = map[string]any{"index_state": backendErr.State}
			apiErr.Cause = backendErr.Cause
			return SearchResult{}, apiErr
		}
		apiErr := NewError(ExitRuntime, "search_failed", "could not search managed documents")
		apiErr.Cause = err
		return SearchResult{}, apiErr
	}
	return SearchResult{
		SchemaVersion:    SchemaVersion,
		Query:            query.Text,
		Backend:          detailed.Backend,
		BackendRequested: detailed.BackendRequested,
		Matching:         detailed.Matching,
		FuzzyExpanded:    detailed.FuzzyExpanded,
		IndexState:       detailed.IndexState,
		Results:          detailed.Results,
		Warnings:         detailed.Warnings,
	}, nil
}

func normalizeReadReference(reference string) (string, error) {
	if !strings.HasPrefix(reference, "lore://") {
		return reference, nil
	}
	parsed, err := url.Parse(reference)
	if err != nil || parsed.Scheme != "lore" || parsed.User != nil || parsed.RawQuery != "" {
		return "", fmt.Errorf("Lore URI is invalid")
	}
	if parsed.Fragment == "" && (parsed.Host == "pages" || parsed.Host == "sources") {
		escapedID := strings.TrimPrefix(parsed.EscapedPath(), "/")
		if escapedID != "" && !strings.Contains(escapedID, "/") {
			id, decodeErr := url.PathUnescape(escapedID)
			if decodeErr == nil && escapedID == url.PathEscape(id) {
				if parsed.Host == "pages" {
					decodeErr = docs.ValidatePageID(id)
				} else {
					decodeErr = docs.ValidateSourceID(id)
				}
				if decodeErr == nil {
					return id, nil
				}
			}
		}
	}
	path := strings.TrimPrefix(parsed.Host+parsed.EscapedPath(), "/")
	path, err = url.PathUnescape(path)
	if err != nil || path == "" {
		return "", fmt.Errorf("Lore URI is invalid")
	}
	return path, nil
}

func lineCount(data []byte) int {
	count := bytes.Count(data, []byte{'\n'})
	if len(data) == 0 || data[len(data)-1] != '\n' {
		count++
	}
	if count == 0 {
		return 1
	}
	return count
}

func sliceLines(data []byte, requested *LineRange) ([]byte, int, int, error) {
	lines := bytes.SplitAfter(data, []byte{'\n'})
	if len(lines) > 1 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		lines = [][]byte{{}}
	}
	start, end := 1, len(lines)
	if requested != nil {
		start, end = requested.Start, requested.End
		if start <= 0 || end <= 0 || end < start {
			return nil, 0, 0, fmt.Errorf("line range must be positive, inclusive, and ordered")
		}
		if start > len(lines) {
			return nil, 0, 0, fmt.Errorf("line range starts at %d but document has %d line(s)", start, len(lines))
		}
		if end > len(lines) {
			end = len(lines)
		}
	}
	return bytes.Join(lines[start-1:end], nil), start, end, nil
}
