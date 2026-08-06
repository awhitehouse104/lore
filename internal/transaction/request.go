package transaction

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"lore/internal/docs"
)

const (
	SchemaVersion        = 1
	MaxRequestBytes      = 16 * 1024 * 1024
	MaxOperations        = 50
	MaxTotalNewContent   = 16 * 1024 * 1024
	MaxDiffBytes         = 16 * 1024 * 1024
	MaxIntegrationPages  = 50
	MaxPatchReplacements = 50
)

type OperationKind string

const (
	OperationCreatePage           OperationKind = "create_page"
	OperationUpdatePage           OperationKind = "update_page"
	OperationPatchPage            OperationKind = "patch_page"
	OperationDeletePage           OperationKind = "delete_page"
	OperationMarkSourceIntegrated OperationKind = "mark_source_integrated"
	OperationSetSourceSensitivity OperationKind = "set_source_sensitivity"
)

type Request struct {
	SchemaVersion int         `json:"schema_version"`
	Message       string      `json:"message"`
	Operations    []Operation `json:"operations"`
}

type Operation struct {
	Op               OperationKind `json:"op"`
	Path             string        `json:"path"`
	ExpectedRevision string        `json:"expected_revision,omitempty"`
	Content          string        `json:"content,omitempty"`
	PageIDs          []string      `json:"page_ids,omitempty"`
	Sensitivity      string        `json:"sensitivity,omitempty"`
	AllowDowngrade   bool          `json:"allow_downgrade,omitempty"`
	Replacements     []Replacement `json:"replacements,omitempty"`
}

type Replacement struct {
	Old string `json:"old"`
	New string `json:"new"`
}

type requestWire struct {
	SchemaVersion int               `json:"schema_version"`
	Message       string            `json:"message"`
	Operations    []json.RawMessage `json:"operations"`
}

type createPageWire struct {
	Op      OperationKind `json:"op"`
	Path    string        `json:"path"`
	Content *string       `json:"content"`
}

type updatePageWire struct {
	Op               OperationKind `json:"op"`
	Path             string        `json:"path"`
	ExpectedRevision string        `json:"expected_revision"`
	Content          *string       `json:"content"`
}

type patchPageWire struct {
	Op               OperationKind `json:"op"`
	Path             string        `json:"path"`
	ExpectedRevision string        `json:"expected_revision"`
	Replacements     []Replacement `json:"replacements"`
}

type deletePageWire struct {
	Op               OperationKind `json:"op"`
	Path             string        `json:"path"`
	ExpectedRevision string        `json:"expected_revision"`
}

type markSourceWire struct {
	Op               OperationKind `json:"op"`
	Path             string        `json:"path"`
	ExpectedRevision string        `json:"expected_revision"`
	PageIDs          []string      `json:"page_ids"`
}

type setSourceSensitivityWire struct {
	Op               OperationKind `json:"op"`
	Path             string        `json:"path"`
	ExpectedRevision string        `json:"expected_revision"`
	Sensitivity      string        `json:"sensitivity"`
	AllowDowngrade   bool          `json:"allow_downgrade,omitempty"`
}

var (
	pageFilenamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*\.md$`)
	revisionPattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	gitHashPattern      = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	messagePrefixes     = []string{
		"integrate:",
		"create:",
		"update:",
		"correct:",
		"archive:",
		"maintenance:",
	}
)

func DecodeRequest(data []byte, maxPageBytes int64) (Request, error) {
	if len(data) > MaxRequestBytes {
		return Request{}, fmt.Errorf("request exceeds %d bytes", MaxRequestBytes)
	}
	var wire requestWire
	if err := decodeStrict(data, &wire); err != nil {
		return Request{}, fmt.Errorf("decode transaction request: %w", err)
	}
	request := Request{
		SchemaVersion: wire.SchemaVersion,
		Message:       wire.Message,
		Operations:    make([]Operation, 0, len(wire.Operations)),
	}
	if wire.SchemaVersion != SchemaVersion {
		return Request{}, fmt.Errorf("schema_version must equal %d", SchemaVersion)
	}
	if err := ValidateMessage(wire.Message); err != nil {
		return Request{}, err
	}
	if len(wire.Operations) == 0 {
		return Request{}, fmt.Errorf("operations must contain at least one operation")
	}
	if len(wire.Operations) > MaxOperations {
		return Request{}, fmt.Errorf("operations must not contain more than %d operations", MaxOperations)
	}
	seenPaths := make(map[string]struct{}, len(wire.Operations))
	totalContent := 0
	for index, raw := range wire.Operations {
		operation, err := decodeOperation(raw)
		if err != nil {
			return Request{}, fmt.Errorf("operation %d: %w", index, err)
		}
		if _, exists := seenPaths[operation.Path]; exists {
			return Request{}, fmt.Errorf("operation %d: duplicate target path %q", index, operation.Path)
		}
		seenPaths[operation.Path] = struct{}{}
		if operation.Op == OperationCreatePage || operation.Op == OperationUpdatePage {
			contentBytes := len([]byte(operation.Content))
			if int64(contentBytes) > maxPageBytes {
				return Request{}, fmt.Errorf("operation %d: resulting page exceeds configured maximum of %d bytes", index, maxPageBytes)
			}
			totalContent += contentBytes
			if totalContent > MaxTotalNewContent {
				return Request{}, fmt.Errorf("total resulting page content exceeds %d bytes", MaxTotalNewContent)
			}
		} else if operation.Op == OperationPatchPage {
			for replacementIndex, replacement := range operation.Replacements {
				if int64(len([]byte(replacement.Old))) > maxPageBytes || int64(len([]byte(replacement.New))) > maxPageBytes {
					return Request{}, fmt.Errorf("operation %d replacement %d exceeds configured maximum of %d bytes", index, replacementIndex, maxPageBytes)
				}
				totalContent += len([]byte(replacement.New))
				if totalContent > MaxTotalNewContent {
					return Request{}, fmt.Errorf("total patch replacement content exceeds %d bytes", MaxTotalNewContent)
				}
			}
		}
		request.Operations = append(request.Operations, operation)
	}
	return request, nil
}

func ValidateMessage(message string) error {
	if !utf8.ValidString(message) {
		return fmt.Errorf("message must be valid UTF-8")
	}
	if len(message) < 1 || len(message) > 160 {
		return fmt.Errorf("message must be between 1 and 160 bytes")
	}
	for _, value := range []byte(message) {
		if value < 0x20 || value == 0x7f {
			return fmt.Errorf("message must contain one line and no ASCII control characters")
		}
	}
	for _, prefix := range messagePrefixes {
		if strings.HasPrefix(message, prefix) {
			return nil
		}
	}
	return fmt.Errorf("message must begin with integrate:, create:, update:, correct:, archive:, or maintenance:")
}

func ValidatePagePath(path string) error {
	if !canonicalRelative(path) || filepath.ToSlash(filepath.Dir(filepath.FromSlash(path))) != "pages" {
		return fmt.Errorf("path must be a direct child of pages/")
	}
	if !pageFilenamePattern.MatchString(filepath.Base(filepath.FromSlash(path))) {
		return fmt.Errorf("page filename must match %s", pageFilenamePattern)
	}
	return nil
}

func ValidateSourcePath(path string) error {
	if !canonicalRelative(path) || !strings.HasPrefix(path, "sources/") || !strings.HasSuffix(path, ".md") {
		return fmt.Errorf("path must be a canonical Markdown path under sources/")
	}
	return nil
}

func ValidateRevision(revision string) error {
	if !revisionPattern.MatchString(revision) {
		return fmt.Errorf("expected_revision must be a lowercase SHA-256 value")
	}
	return nil
}

func ValidateGitHash(hash string) error {
	if !gitHashPattern.MatchString(hash) {
		return fmt.Errorf("Git hash must be a full lowercase 40- or 64-character hexadecimal object ID")
	}
	return nil
}

func ValidateTransactionID(value string) error {
	if !strings.HasPrefix(value, "tx_") || len(value) != 29 {
		return fmt.Errorf("transaction ID must be tx_ followed by a 26-character canonical ULID")
	}
	if err := docs.ValidateSourceID("src_" + strings.TrimPrefix(value, "tx_")); err != nil {
		return fmt.Errorf("transaction ID must be tx_ followed by a 26-character canonical ULID")
	}
	return nil
}

func decodeOperation(raw []byte) (Operation, error) {
	var discriminator struct {
		Op OperationKind `json:"op"`
	}
	if err := json.Unmarshal(raw, &discriminator); err != nil {
		return Operation{}, fmt.Errorf("decode operation: %w", err)
	}
	switch discriminator.Op {
	case OperationCreatePage:
		var wire createPageWire
		if err := decodeStrict(raw, &wire); err != nil {
			return Operation{}, fmt.Errorf("decode create_page: %w", err)
		}
		if err := ValidatePagePath(wire.Path); err != nil {
			return Operation{}, err
		}
		if wire.Content == nil {
			return Operation{}, fmt.Errorf("content is required")
		}
		if !utf8.ValidString(*wire.Content) {
			return Operation{}, fmt.Errorf("content must be valid UTF-8")
		}
		return Operation{Op: wire.Op, Path: wire.Path, Content: *wire.Content}, nil
	case OperationUpdatePage:
		var wire updatePageWire
		if err := decodeStrict(raw, &wire); err != nil {
			return Operation{}, fmt.Errorf("decode update_page: %w", err)
		}
		if err := ValidatePagePath(wire.Path); err != nil {
			return Operation{}, err
		}
		if err := ValidateRevision(wire.ExpectedRevision); err != nil {
			return Operation{}, err
		}
		if wire.Content == nil {
			return Operation{}, fmt.Errorf("content is required")
		}
		if !utf8.ValidString(*wire.Content) {
			return Operation{}, fmt.Errorf("content must be valid UTF-8")
		}
		return Operation{
			Op:               wire.Op,
			Path:             wire.Path,
			ExpectedRevision: wire.ExpectedRevision,
			Content:          *wire.Content,
		}, nil
	case OperationPatchPage:
		var wire patchPageWire
		if err := decodeStrict(raw, &wire); err != nil {
			return Operation{}, fmt.Errorf("decode patch_page: %w", err)
		}
		if err := ValidatePagePath(wire.Path); err != nil {
			return Operation{}, err
		}
		if err := ValidateRevision(wire.ExpectedRevision); err != nil {
			return Operation{}, err
		}
		if len(wire.Replacements) < 1 || len(wire.Replacements) > MaxPatchReplacements {
			return Operation{}, fmt.Errorf("replacements must contain between 1 and %d entries", MaxPatchReplacements)
		}
		seen := make(map[string]struct{}, len(wire.Replacements))
		for index, replacement := range wire.Replacements {
			if replacement.Old == "" {
				return Operation{}, fmt.Errorf("replacement %d old text must not be empty", index)
			}
			if !utf8.ValidString(replacement.Old) || !utf8.ValidString(replacement.New) {
				return Operation{}, fmt.Errorf("replacement %d text must be valid UTF-8", index)
			}
			if replacement.Old == replacement.New {
				return Operation{}, fmt.Errorf("replacement %d must change the matched text", index)
			}
			if _, exists := seen[replacement.Old]; exists {
				return Operation{}, fmt.Errorf("replacements must contain unique old text")
			}
			seen[replacement.Old] = struct{}{}
		}
		return Operation{
			Op:               wire.Op,
			Path:             wire.Path,
			ExpectedRevision: wire.ExpectedRevision,
			Replacements:     append([]Replacement(nil), wire.Replacements...),
		}, nil
	case OperationDeletePage:
		var wire deletePageWire
		if err := decodeStrict(raw, &wire); err != nil {
			return Operation{}, fmt.Errorf("decode delete_page: %w", err)
		}
		if err := ValidatePagePath(wire.Path); err != nil {
			return Operation{}, err
		}
		if err := ValidateRevision(wire.ExpectedRevision); err != nil {
			return Operation{}, err
		}
		return Operation{
			Op:               wire.Op,
			Path:             wire.Path,
			ExpectedRevision: wire.ExpectedRevision,
		}, nil
	case OperationMarkSourceIntegrated:
		var wire markSourceWire
		if err := decodeStrict(raw, &wire); err != nil {
			return Operation{}, fmt.Errorf("decode mark_source_integrated: %w", err)
		}
		if err := ValidateSourcePath(wire.Path); err != nil {
			return Operation{}, err
		}
		if err := ValidateRevision(wire.ExpectedRevision); err != nil {
			return Operation{}, err
		}
		if len(wire.PageIDs) == 0 || len(wire.PageIDs) > MaxIntegrationPages {
			return Operation{}, fmt.Errorf("page_ids must contain between 1 and %d page IDs", MaxIntegrationPages)
		}
		seen := make(map[string]struct{}, len(wire.PageIDs))
		for _, pageID := range wire.PageIDs {
			if err := docs.ValidatePageID(pageID); err != nil {
				return Operation{}, fmt.Errorf("invalid page ID %q", pageID)
			}
			if _, exists := seen[pageID]; exists {
				return Operation{}, fmt.Errorf("page_ids must contain unique page IDs")
			}
			seen[pageID] = struct{}{}
		}
		return Operation{
			Op:               wire.Op,
			Path:             wire.Path,
			ExpectedRevision: wire.ExpectedRevision,
			PageIDs:          append([]string(nil), wire.PageIDs...),
		}, nil
	case OperationSetSourceSensitivity:
		var wire setSourceSensitivityWire
		if err := decodeStrict(raw, &wire); err != nil {
			return Operation{}, fmt.Errorf("decode set_source_sensitivity: %w", err)
		}
		if err := ValidateSourcePath(wire.Path); err != nil {
			return Operation{}, err
		}
		if err := ValidateRevision(wire.ExpectedRevision); err != nil {
			return Operation{}, err
		}
		if !docs.ValidSensitivity(wire.Sensitivity) {
			return Operation{}, fmt.Errorf("sensitivity must be normal, sensitive, or local-only")
		}
		return Operation{
			Op:               wire.Op,
			Path:             wire.Path,
			ExpectedRevision: wire.ExpectedRevision,
			Sensitivity:      wire.Sensitivity,
			AllowDowngrade:   wire.AllowDowngrade,
		}, nil
	default:
		return Operation{}, fmt.Errorf("op must be create_page, update_page, patch_page, delete_page, mark_source_integrated, or set_source_sensitivity")
	}
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func canonicalRelative(path string) bool {
	if path == "" || strings.ContainsRune(path, '\x00') || strings.Contains(path, "\\") || strings.HasPrefix(path, "/") {
		return false
	}
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))) == path && path != "."
}
