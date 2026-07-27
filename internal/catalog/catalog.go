package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"lore/internal/docs"
	"lore/internal/repository"
)

type Warning struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

type Catalog struct {
	Documents []*docs.Document
}

type AmbiguousError struct {
	Reference  string
	Rule       string
	Candidates []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("reference %q is ambiguous by %s: %s", e.Reference, e.Rule, strings.Join(e.Candidates, ", "))
}

type NotFoundError struct {
	Reference string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("no managed document matches reference %q", e.Reference)
}

type UnsafeReferenceError struct {
	Reference string
	Message   string
}

func (e *UnsafeReferenceError) Error() string {
	return fmt.Sprintf("unsafe reference %q: %s", e.Reference, e.Message)
}

func Scan(repo *repository.Repository) (Catalog, []Warning, error) {
	paths, _, err := repo.ManagedMarkdown()
	if err != nil {
		return Catalog{}, nil, err
	}
	sort.Strings(paths)
	catalog := Catalog{Documents: make([]*docs.Document, 0, len(paths))}
	warnings := []Warning{}
	for _, path := range paths {
		absolute, err := repo.SafeContentPath(path)
		if err != nil {
			warnings = append(warnings, Warning{Code: "unsafe_document_skipped", Path: path, Message: err.Error()})
			continue
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return Catalog{}, nil, fmt.Errorf("inspect %s: %w", path, err)
		}
		if info.Size() > repo.Config.Capture.MaxBytes {
			warnings = append(warnings, Warning{
				Code:    "document_too_large",
				Path:    path,
				Message: fmt.Sprintf("document exceeds configured maximum of %d bytes", repo.Config.Capture.MaxBytes),
			})
			continue
		}
		data, err := os.ReadFile(absolute)
		if err != nil {
			return Catalog{}, nil, fmt.Errorf("read %s: %w", path, err)
		}
		document, err := docs.Parse(path, data)
		if err != nil {
			warnings = append(warnings, Warning{Code: "invalid_document_skipped", Path: path, Message: err.Error()})
			continue
		}
		catalog.Documents = append(catalog.Documents, document)
	}
	return catalog, warnings, nil
}

func (c Catalog) Resolve(repo *repository.Repository, reference string) (*docs.Document, error) {
	if reference == "" {
		return nil, &NotFoundError{Reference: reference}
	}
	if strings.ContainsRune(reference, '\x00') || filepath.IsAbs(reference) || hasTraversal(reference) {
		return nil, &UnsafeReferenceError{Reference: reference, Message: "absolute paths, NUL bytes, and .. traversal are not allowed"}
	}

	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(reference)))
	if strings.HasPrefix(clean, "pages/") || strings.HasPrefix(clean, "sources/") {
		if _, err := repo.SafeContentPath(clean); err != nil {
			return nil, &UnsafeReferenceError{Reference: reference, Message: err.Error()}
		}
		if document, err := selectMatch(reference, "exact path", c.Documents, func(document *docs.Document) bool {
			return document.Path == clean
		}); document != nil || err != nil {
			return document, err
		}
	}

	rules := []struct {
		name  string
		match func(*docs.Document) bool
	}{
		{
			name: "document ID",
			match: func(document *docs.Document) bool {
				return document.ID() == reference
			},
		},
		{
			name: "filename stem",
			match: func(document *docs.Document) bool {
				return strings.TrimSuffix(filepath.Base(document.Path), filepath.Ext(document.Path)) == reference
			},
		},
		{
			name: "page title",
			match: func(document *docs.Document) bool {
				return document.Page != nil && strings.EqualFold(document.Page.Title, reference)
			},
		},
		{
			name: "page alias",
			match: func(document *docs.Document) bool {
				if document.Page == nil {
					return false
				}
				for _, alias := range document.Page.Aliases {
					if strings.EqualFold(alias, reference) {
						return true
					}
				}
				return false
			},
		},
	}
	for _, rule := range rules {
		document, err := selectMatch(reference, rule.name, c.Documents, rule.match)
		if document != nil || err != nil {
			return document, err
		}
	}
	return nil, &NotFoundError{Reference: reference}
}

func selectMatch(reference, rule string, documents []*docs.Document, match func(*docs.Document) bool) (*docs.Document, error) {
	matches := make([]*docs.Document, 0, 1)
	for _, document := range documents {
		if match(document) {
			matches = append(matches, document)
		}
	}
	if len(matches) == 0 {
		return nil, nil
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	candidates := make([]string, 0, len(matches))
	for _, document := range matches {
		candidates = append(candidates, document.Path)
	}
	sort.Strings(candidates)
	return nil, &AmbiguousError{Reference: reference, Rule: rule, Candidates: candidates}
}

func hasTraversal(reference string) bool {
	for _, part := range strings.FieldsFunc(filepath.ToSlash(reference), func(r rune) bool { return r == '/' }) {
		if part == ".." {
			return true
		}
	}
	return false
}
