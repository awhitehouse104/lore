package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	LineStart     int    `json:"line_start"`
	LineEnd       int    `json:"line_end"`
	Revision      string `json:"revision"`
	Content       string `json:"content"`
}

type SearchResult struct {
	SchemaVersion int               `json:"schema_version"`
	Query         string            `json:"query"`
	Results       []search.Result   `json:"results"`
	Warnings      []catalog.Warning `json:"warnings"`
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

func (s *Service) Read(_ context.Context, reference string, requested *LineRange) (ReadResult, error) {
	if s == nil || s.Repo == nil {
		return ReadResult{}, NewError(ExitRuntime, "service_unavailable", "read service is not fully configured")
	}
	documentCatalog, _, err := catalog.Scan(s.Repo)
	if err != nil {
		apiErr := NewError(ExitRuntime, "catalog_scan_failed", "could not scan managed documents")
		apiErr.Cause = err
		return ReadResult{}, apiErr
	}
	document, err := documentCatalog.Resolve(s.Repo, reference)
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
		LineStart:     lineStart,
		LineEnd:       lineEnd,
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
	results, warnings, err := s.Searcher.Search(ctx, s.Repo, query)
	if err != nil {
		apiErr := NewError(ExitValidation, "invalid_search", err.Error())
		apiErr.Cause = err
		return SearchResult{}, apiErr
	}
	return SearchResult{
		SchemaVersion: SchemaVersion,
		Query:         query.Text,
		Results:       results,
		Warnings:      warnings,
	}, nil
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
