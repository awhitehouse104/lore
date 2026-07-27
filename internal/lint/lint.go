package lint

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"lore/internal/docs"
	"lore/internal/gitx"
	"lore/internal/repository"
)

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

type Finding struct {
	Severity     Severity `json:"severity"`
	Code         string   `json:"code"`
	Path         string   `json:"path"`
	Line         int      `json:"line"`
	Message      string   `json:"message"`
	RelatedPaths []string `json:"related_paths,omitempty"`
}

type Result struct {
	SchemaVersion int       `json:"schema_version"`
	Valid         bool      `json:"valid"`
	Errors        int       `json:"errors"`
	Warnings      int       `json:"warnings"`
	Findings      []Finding `json:"findings"`
}

var sourceFilenamePattern = regexp.MustCompile(`^(src_[0-9A-Z]{26})-([a-z][a-z0-9_-]*)\.md$`)

func RunRoot(ctx context.Context, root string, git gitx.Client) (Result, error) {
	repo, err := repository.Open(root)
	if err == nil {
		return Run(ctx, repo, git)
	}
	configPath := filepath.Join(root, "lore.yaml")
	_, statErr := os.Stat(configPath)
	code := "invalid_config"
	message := err.Error()
	if os.IsNotExist(statErr) {
		code = "missing_config"
		message = "lore.yaml is missing"
	} else if statErr != nil {
		return Result{}, fmt.Errorf("inspect lore.yaml: %w", statErr)
	}
	result := Result{
		SchemaVersion: 1,
		Findings: []Finding{{
			Severity: SeverityError,
			Code:     code,
			Path:     "lore.yaml",
			Line:     1,
			Message:  message,
		}},
	}
	finish(&result)
	return result, nil
}

func Run(ctx context.Context, repo *repository.Repository, git gitx.Client) (Result, error) {
	result := Result{SchemaVersion: 1, Valid: true, Findings: []Finding{}}
	for _, dir := range []string{"pages", "sources", "assets", "system", ".lore"} {
		info, err := os.Lstat(filepath.Join(repo.Root, dir))
		if err != nil {
			if os.IsNotExist(err) {
				result.Findings = append(result.Findings, Finding{
					Severity: SeverityError,
					Code:     "missing_required_directory",
					Path:     dir,
					Message:  "required directory is missing",
				})
				continue
			}
			return Result{}, fmt.Errorf("inspect required directory %s: %w", dir, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			result.Findings = append(result.Findings, Finding{
				Severity: SeverityError,
				Code:     "required_directory_not_directory",
				Path:     dir,
				Message:  "required repository path is not a directory",
			})
		}
	}

	isGit, err := git.IsRepository(ctx, repo.Root)
	if err != nil {
		return Result{}, err
	}
	if isGit {
		ignored, ignoreErr := git.IsIgnored(ctx, repo.Root, ".lore/")
		if ignoreErr != nil {
			return Result{}, ignoreErr
		}
		if !ignored {
			result.Findings = append(result.Findings, Finding{
				Severity: SeverityError,
				Code:     "runtime_state_not_ignored",
				Path:     ".gitignore",
				Message:  ".lore/ must be ignored by Git",
			})
		}
	}

	paths, walkIssues, err := repo.ManagedMarkdown()
	if err != nil {
		return Result{}, err
	}
	for _, issue := range walkIssues {
		result.Findings = append(result.Findings, Finding{
			Severity: SeverityError,
			Code:     issue.Code,
			Path:     issue.Path,
			Message:  issue.Message,
		})
	}
	sort.Strings(paths)

	documents := make([]*docs.Document, 0, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		absolute, pathErr := repo.SafeContentPath(path)
		if pathErr != nil {
			result.Findings = append(result.Findings, Finding{
				Severity: SeverityError,
				Code:     "unsafe_managed_path",
				Path:     path,
				Message:  pathErr.Error(),
			})
			continue
		}
		info, statErr := os.Stat(absolute)
		if statErr != nil {
			return Result{}, fmt.Errorf("inspect %s: %w", path, statErr)
		}
		if info.Size() > repo.Config.Capture.MaxBytes {
			result.Findings = append(result.Findings, Finding{
				Severity: SeverityWarning,
				Code:     "managed_file_too_large",
				Path:     path,
				Message:  fmt.Sprintf("managed file is %d bytes; configured maximum is %d", info.Size(), repo.Config.Capture.MaxBytes),
			})
			continue
		}
		data, readErr := os.ReadFile(absolute)
		if readErr != nil {
			return Result{}, fmt.Errorf("read %s: %w", path, readErr)
		}
		if !utf8.Valid(data) {
			result.Findings = append(result.Findings, Finding{
				Severity: SeverityError,
				Code:     "invalid_utf8",
				Path:     path,
				Message:  "managed Markdown is not valid UTF-8",
			})
			continue
		}
		document, parseErr := docs.Parse(path, data)
		if parseErr != nil {
			result.Findings = append(result.Findings, Finding{
				Severity: SeverityError,
				Code:     "invalid_frontmatter",
				Path:     path,
				Line:     1,
				Message:  parseErr.Error(),
			})
			continue
		}
		documents = append(documents, document)
		for _, validationErr := range docs.Validate(document) {
			result.Findings = append(result.Findings, Finding{
				Severity: SeverityError,
				Code:     "invalid_metadata",
				Path:     path,
				Line:     2,
				Message:  validationErr.Error(),
			})
		}
		if document.Source != nil {
			checkSource(&result, document)
		}
	}

	checkDuplicateIDs(&result, documents)
	checkAliasCollisions(&result, documents)
	checkLinks(&result, repo, documents)
	if isGit {
		changes, changesErr := git.SourceChanges(ctx, repo.Root)
		if changesErr != nil {
			return Result{}, changesErr
		}
		for _, change := range changes {
			result.Findings = append(result.Findings, Finding{
				Severity: SeverityWarning,
				Code:     "uncommitted_source_change",
				Path:     change.Path,
				Message:  fmt.Sprintf("source has uncommitted Git status %q", change.Status),
			})
		}
		_, detached, branchErr := git.BranchState(ctx, repo.Root)
		if branchErr != nil {
			return Result{}, branchErr
		}
		if detached {
			result.Findings = append(result.Findings, Finding{
				Severity: SeverityWarning,
				Code:     "detached_head",
				Path:     ".git/HEAD",
				Message:  "Git HEAD is detached",
			})
		}
	}
	finish(&result)
	return result, nil
}

func checkSource(result *Result, document *docs.Document) {
	source := document.Source
	if docs.SHA256(document.Body) != source.RawSHA256 {
		result.Findings = append(result.Findings, Finding{
			Severity: SeverityError,
			Code:     "source_body_modified",
			Path:     document.Path,
			Line:     bodyLine(document.Data, document.BodyOffset),
			Message:  "source body SHA-256 does not match raw_sha256",
		})
	}

	parts := strings.Split(filepath.ToSlash(document.Path), "/")
	if len(parts) != 4 {
		result.Findings = append(result.Findings, Finding{
			Severity: SeverityError,
			Code:     "invalid_source_path",
			Path:     document.Path,
			Message:  "source path must be sources/YYYY/MM/<filename>.md",
		})
		return
	}
	captured, timeErr := time.Parse(time.RFC3339, string(source.CapturedAt))
	if timeErr == nil {
		utc := captured.UTC()
		if parts[1] != utc.Format("2006") || parts[2] != utc.Format("01") {
			result.Findings = append(result.Findings, Finding{
				Severity: SeverityError,
				Code:     "source_date_path_mismatch",
				Path:     document.Path,
				Message:  "source path year/month does not match captured_at in UTC",
			})
		}
	}
	filenameMatch := sourceFilenamePattern.FindStringSubmatch(filepath.Base(document.Path))
	if len(filenameMatch) != 3 {
		result.Findings = append(result.Findings, Finding{
			Severity: SeverityError,
			Code:     "invalid_source_filename",
			Path:     document.Path,
			Message:  "source filename must contain its source ID and kind",
		})
		return
	}
	if filenameMatch[1] != source.ID || filenameMatch[2] != source.Kind {
		result.Findings = append(result.Findings, Finding{
			Severity: SeverityError,
			Code:     "source_filename_metadata_mismatch",
			Path:     document.Path,
			Message:  "source filename ID and kind must match frontmatter",
		})
	}
}

func checkDuplicateIDs(result *Result, documents []*docs.Document) {
	byID := map[string][]string{}
	for _, document := range documents {
		if document.ID() != "" {
			byID[document.ID()] = append(byID[document.ID()], document.Path)
		}
	}
	for id, paths := range byID {
		if len(paths) < 2 {
			continue
		}
		sort.Strings(paths)
		for _, path := range paths {
			related := except(paths, path)
			result.Findings = append(result.Findings, Finding{
				Severity:     SeverityError,
				Code:         "duplicate_id",
				Path:         path,
				Line:         2,
				Message:      fmt.Sprintf("document ID %s is also used by %s", id, strings.Join(related, ", ")),
				RelatedPaths: related,
			})
		}
	}
}

func checkAliasCollisions(result *Result, documents []*docs.Document) {
	claims := map[string]map[string]struct{}{}
	for _, document := range documents {
		if document.Page == nil {
			continue
		}
		values := append([]string{document.Page.Title}, document.Page.Aliases...)
		for _, value := range values {
			key := strings.ToLower(strings.TrimSpace(value))
			if key == "" {
				continue
			}
			if claims[key] == nil {
				claims[key] = map[string]struct{}{}
			}
			claims[key][document.Path] = struct{}{}
		}
	}
	for value, pathSet := range claims {
		if len(pathSet) < 2 {
			continue
		}
		paths := keys(pathSet)
		for _, path := range paths {
			related := except(paths, path)
			result.Findings = append(result.Findings, Finding{
				Severity:     SeverityError,
				Code:         "ambiguous_page_name",
				Path:         path,
				Line:         2,
				Message:      fmt.Sprintf("page title or alias %q also identifies %s", value, strings.Join(related, ", ")),
				RelatedPaths: related,
			})
		}
	}
}

func checkLinks(result *Result, repo *repository.Repository, documents []*docs.Document) {
	for _, document := range documents {
		bodyStart := bytes.Count(document.Data[:document.BodyOffset], []byte{'\n'}) + 1
		for index, line := range strings.Split(string(document.Body), "\n") {
			for _, destination := range markdownDestinations(line) {
				checkLink(result, repo, document.Path, bodyStart+index, destination)
			}
		}
	}
}

func checkLink(result *Result, repo *repository.Repository, documentPath string, line int, destination string) {
	if destination == "" || strings.HasPrefix(destination, "#") {
		return
	}
	parsed, err := url.Parse(destination)
	if err != nil {
		result.Findings = append(result.Findings, Finding{
			Severity: SeverityError,
			Code:     "invalid_link",
			Path:     documentPath,
			Line:     line,
			Message:  fmt.Sprintf("invalid Markdown link %q", destination),
		})
		return
	}
	if parsed.Scheme != "" || parsed.Host != "" {
		return
	}
	targetPath, err := url.PathUnescape(parsed.Path)
	if err != nil {
		result.Findings = append(result.Findings, Finding{
			Severity: SeverityError,
			Code:     "invalid_link",
			Path:     documentPath,
			Line:     line,
			Message:  fmt.Sprintf("invalid escaped Markdown link %q", destination),
		})
		return
	}
	if targetPath == "" {
		return
	}
	if filepath.IsAbs(filepath.FromSlash(targetPath)) {
		result.Findings = append(result.Findings, Finding{
			Severity: SeverityError,
			Code:     "link_escapes_repository",
			Path:     documentPath,
			Line:     line,
			Message:  fmt.Sprintf("relative Markdown link %q is absolute", destination),
		})
		return
	}
	relative := filepath.Clean(filepath.Join(filepath.Dir(filepath.FromSlash(documentPath)), filepath.FromSlash(targetPath)))
	absolute, safeErr := repo.SafeRepositoryPath(relative)
	if safeErr != nil {
		result.Findings = append(result.Findings, Finding{
			Severity: SeverityError,
			Code:     "link_escapes_repository",
			Path:     documentPath,
			Line:     line,
			Message:  fmt.Sprintf("Markdown link %q is unsafe: %v", destination, safeErr),
		})
		return
	}
	if _, statErr := os.Stat(absolute); statErr != nil {
		if os.IsNotExist(statErr) {
			result.Findings = append(result.Findings, Finding{
				Severity: SeverityError,
				Code:     "broken_link",
				Path:     documentPath,
				Line:     line,
				Message:  fmt.Sprintf("Markdown link target %q does not exist", destination),
			})
			return
		}
		result.Findings = append(result.Findings, Finding{
			Severity: SeverityError,
			Code:     "link_target_error",
			Path:     documentPath,
			Line:     line,
			Message:  fmt.Sprintf("could not inspect Markdown link target %q: %v", destination, statErr),
		})
	}
}

func markdownDestinations(line string) []string {
	var destinations []string
	for searchFrom := 0; searchFrom < len(line); {
		relative := strings.Index(line[searchFrom:], "](")
		if relative < 0 {
			break
		}
		open := searchFrom + relative + 1
		start := open + 1
		depth := 1
		escaped := false
		closeIndex := -1
		for index := start; index < len(line); index++ {
			value := line[index]
			if escaped {
				escaped = false
				continue
			}
			if value == '\\' {
				escaped = true
				continue
			}
			switch value {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					closeIndex = index
				}
			}
			if closeIndex >= 0 {
				break
			}
		}
		if closeIndex < 0 {
			break
		}
		destination := extractMarkdownDestination(strings.TrimSpace(line[start:closeIndex]))
		destinations = append(destinations, destination)
		searchFrom = closeIndex + 1
	}
	if destination, ok := referenceDefinitionDestination(line); ok {
		destinations = append(destinations, destination)
	}
	return destinations
}

func referenceDefinitionDestination(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if len(line)-len(trimmed) > 3 || !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	end := strings.Index(trimmed, "]:")
	if end <= 1 {
		return "", false
	}
	raw := strings.TrimSpace(trimmed[end+2:])
	if raw == "" {
		return "", false
	}
	return extractMarkdownDestination(raw), true
}

func extractMarkdownDestination(raw string) string {
	destination := raw
	if strings.HasPrefix(raw, "<") {
		if end := strings.Index(raw, ">"); end >= 0 {
			destination = raw[1:end]
		}
	} else if end := strings.IndexAny(raw, " \t"); end >= 0 {
		destination = raw[:end]
	}
	return unescapeMarkdownDestination(destination)
}

func unescapeMarkdownDestination(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	escaped := false
	for _, r := range value {
		if escaped {
			builder.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		builder.WriteRune(r)
	}
	if escaped {
		builder.WriteRune('\\')
	}
	return builder.String()
}

func finish(result *Result) {
	sort.Slice(result.Findings, func(i, j int) bool {
		a, b := result.Findings[i], result.Findings[j]
		if severityRank(a.Severity) != severityRank(b.Severity) {
			return severityRank(a.Severity) < severityRank(b.Severity)
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Message < b.Message
	})
	for _, finding := range result.Findings {
		if finding.Severity == SeverityError {
			result.Errors++
		} else {
			result.Warnings++
		}
	}
	result.Valid = result.Errors == 0
}

func severityRank(severity Severity) int {
	if severity == SeverityError {
		return 0
	}
	return 1
}

func bodyLine(data []byte, offset int) int {
	line := 1
	for _, value := range data[:offset] {
		if value == '\n' {
			line++
		}
	}
	return line
}

func except(values []string, excluded string) []string {
	out := make([]string, 0, len(values)-1)
	for _, value := range values {
		if value != excluded {
			out = append(out, value)
		}
	}
	return out
}

func keys(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
