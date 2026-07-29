package index

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"lore/internal/docs"
)

const maximumDocumentDiagnostics = 50

type indexedDocument struct {
	Path          string
	DocumentID    string
	DocumentType  string
	Title         string
	Kind          string
	Sensitivity   string
	AliasesText   string
	TagsText      string
	AliasesJSON   string
	TagsJSON      string
	Body          string
	BodyLineStart int
	Revision      string
	ContentSHA256 string
	CreatedAt     string
	UpdatedAt     string
	IndexedAt     string
}

func (m *Manager) scanDocuments(ctx context.Context, indexedAt string) ([]indexedDocument, error) {
	paths, issues, err := m.Repo.ManagedMarkdown()
	if err != nil {
		return nil, fmt.Errorf("enumerate managed Markdown: %w", err)
	}
	sort.Strings(paths)
	diagnostics := make([]string, 0)
	for _, issue := range issues {
		diagnostics = appendDiagnostic(diagnostics, issue.Path+": "+issue.Message)
	}
	documents := make([]indexedDocument, 0, len(paths))
	seenIDs := map[string]string{}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		absolute, err := m.Repo.SafeContentPath(path)
		if err != nil {
			diagnostics = appendDiagnostic(diagnostics, path+": unsafe managed path")
			continue
		}
		data, err := os.ReadFile(absolute)
		if err != nil {
			return nil, fmt.Errorf("read managed document %s: %w", path, err)
		}
		document, err := docs.Parse(path, data)
		if err != nil {
			diagnostics = appendDiagnostic(diagnostics, path+": "+err.Error())
			continue
		}
		validation := docs.Validate(document)
		for _, validationErr := range validation {
			diagnostics = appendDiagnostic(diagnostics, path+": "+validationErr.Error())
		}
		if document.Source != nil && docs.SHA256(document.Body) != document.Source.RawSHA256 {
			diagnostics = appendDiagnostic(diagnostics, path+": source body SHA-256 does not match raw_sha256")
		}
		if previous, exists := seenIDs[document.ID()]; exists {
			diagnostics = appendDiagnostic(diagnostics, path+": document ID is also used by "+previous)
		} else {
			seenIDs[document.ID()] = path
		}
		createdAt, updatedAt := documentTimes(document)
		aliasesJSON, err := encodeValues(document.Aliases())
		if err != nil {
			return nil, fmt.Errorf("encode aliases for %s: %w", path, err)
		}
		tagsJSON, err := encodeValues(document.Tags())
		if err != nil {
			return nil, fmt.Errorf("encode tags for %s: %w", path, err)
		}
		documents = append(documents, indexedDocument{
			Path:          path,
			DocumentID:    document.ID(),
			DocumentType:  string(document.Type),
			Title:         document.Title(),
			Kind:          document.Kind(),
			Sensitivity:   document.Sensitivity(),
			AliasesText:   flattenValues(document.Aliases()),
			TagsText:      flattenValues(document.Tags()),
			AliasesJSON:   aliasesJSON,
			TagsJSON:      tagsJSON,
			Body:          string(document.Body),
			BodyLineStart: bytes.Count(document.Data[:document.BodyOffset], []byte{'\n'}) + 1,
			Revision:      docs.Revision(document.Data),
			ContentSHA256: docs.Revision(document.Data),
			CreatedAt:     createdAt,
			UpdatedAt:     updatedAt,
			IndexedAt:     indexedAt,
		})
	}
	if len(diagnostics) > 0 {
		message := fmt.Sprintf("%d canonical document validation problem(s) prevent indexing", len(diagnostics))
		return nil, newError(ErrorValidation, "invalid_canonical_documents", message+": "+strings.Join(diagnostics, "; "), nil)
	}
	return documents, nil
}

func appendDiagnostic(values []string, value string) []string {
	if len(values) >= maximumDocumentDiagnostics {
		return values
	}
	return append(values, value)
}

func flattenValues(values []string) string {
	return strings.Join(values, "\n")
}

func encodeValues(values []string) (string, error) {
	if values == nil {
		values = []string{}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func documentTimes(document *docs.Document) (string, string) {
	if document.Page != nil {
		return string(document.Page.Created), string(document.Page.Updated)
	}
	if document.Source != nil {
		return string(document.Source.CapturedAt), string(document.Source.IntegratedAt)
	}
	return "", ""
}
