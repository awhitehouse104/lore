package search

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"lore/internal/catalog"
	"lore/internal/docs"
	"lore/internal/repository"
)

const (
	DefaultLimit = 10
	MaximumLimit = 100
	snippetLimit = 500
	bodyTokenCap = 10
)

type Scope string

const (
	ScopeAll     Scope = "all"
	ScopePages   Scope = "pages"
	ScopeSources Scope = "sources"
)

type Query struct {
	Text     string
	Scope    Scope
	Kind     string
	Tags     []string
	Paths    []string
	Limit    int
	Backend  Backend
	Matching MatchingMode
	Access   AccessPolicy
}

type Result struct {
	Rank         int          `json:"rank"`
	Score        int          `json:"score"`
	Path         string       `json:"path"`
	URI          string       `json:"uri"`
	ResourceURI  string       `json:"resource_uri"`
	ID           string       `json:"id"`
	Title        string       `json:"title"`
	Kind         string       `json:"kind"`
	LineStart    int          `json:"line_start"`
	LineEnd      int          `json:"line_end"`
	Snippet      string       `json:"snippet"`
	Revision     string       `json:"revision"`
	FuzzyMatches []FuzzyMatch `json:"fuzzy_matches,omitempty"`
}

type Searcher interface {
	Search(context.Context, *repository.Repository, Query) ([]Result, []catalog.Warning, error)
}

type FilesystemLexicalSearcher struct{}

func (FilesystemLexicalSearcher) Search(ctx context.Context, repo *repository.Repository, query Query) ([]Result, []catalog.Warning, error) {
	if err := ValidateQuery(query); err != nil {
		return nil, nil, err
	}
	if query.Scope == "" {
		query.Scope = ScopeAll
	}
	if query.Limit == 0 {
		query.Limit = DefaultLimit
	}
	query.Matching = NormalizeMatching(query.Matching)

	documentCatalog, warnings, err := catalog.Scan(ctx, repo, true)
	if err != nil {
		return nil, nil, err
	}
	candidates := make([]Candidate, 0, len(documentCatalog.Documents))
	for _, document := range documentCatalog.Documents {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if query.Scope == ScopePages && document.Type != docs.TypePage {
			continue
		}
		if query.Scope == ScopeSources && document.Type != docs.TypeSource {
			continue
		}
		if query.Kind != "" && document.Kind() != query.Kind {
			continue
		}
		if !matchesTags(document.Tags(), query.Tags) || !matchesPath(document.Path, query.Paths) {
			continue
		}
		if !query.Access.Allows(document.Sensitivity()) {
			continue
		}
		candidates = append(candidates, Candidate{
			Path:          document.Path,
			DocumentID:    document.ID(),
			DocumentType:  document.Type,
			Title:         document.Title(),
			Kind:          document.Kind(),
			Sensitivity:   document.Sensitivity(),
			Aliases:       document.Aliases(),
			Tags:          document.Tags(),
			Body:          document.Body,
			BodyLineStart: bytes.Count(document.Data[:document.BodyOffset], []byte{'\n'}) + 1,
			Revision:      docs.Revision(document.Data),
		})
	}
	tokens := uniqueTokens(tokenize(query.Text))
	documentFrequency, expansions, fuzzyWarnings, err := prepareFuzzyExpansion(candidates, tokens, query.Matching)
	if err != nil {
		return nil, nil, err
	}
	warnings = append(warnings, fuzzyWarnings...)
	return rankCandidates(candidates, query, len(candidates), documentFrequency, expansions), warnings, nil
}

func resourceURI(documentType docs.Type, id string) string {
	return fmt.Sprintf("lore://%ss/%s", documentType, url.PathEscape(id))
}

func ValidateQuery(query Query) error {
	if query.Limit < 0 || query.Limit > MaximumLimit {
		return fmt.Errorf("search limit must be between 1 and %d", MaximumLimit)
	}
	switch query.Scope {
	case "", ScopeAll, ScopePages, ScopeSources:
	default:
		return fmt.Errorf("search scope must be all, pages, or sources")
	}
	switch query.Backend {
	case "", BackendAuto, BackendIndex, BackendFilesystem:
	default:
		return fmt.Errorf("search backend must be auto, index, or filesystem")
	}
	switch query.Matching {
	case "", MatchingAuto, MatchingLexical, MatchingFuzzy:
	default:
		return fmt.Errorf("search matching must be auto, lexical, or fuzzy")
	}
	if NormalizeMatching(query.Matching) == MatchingFuzzy {
		eligible := 0
		for _, token := range uniqueTokens(tokenize(query.Text)) {
			length := utf8.RuneCountInString(token)
			if length >= MinimumFuzzyTokenRunes && length <= MaximumFuzzyTokenRunes {
				eligible++
			}
		}
		if eligible > MaximumFuzzyQueryTokens {
			return &MatchingError{
				Code:    "fuzzy_query_too_broad",
				Message: fmt.Sprintf("fuzzy matching supports at most %d eligible query terms", MaximumFuzzyQueryTokens),
			}
		}
	}
	if err := query.Access.Validate(); err != nil {
		return err
	}
	for _, tag := range query.Tags {
		if strings.TrimSpace(tag) == "" {
			return fmt.Errorf("search tags must be non-empty")
		}
	}
	for _, path := range query.Paths {
		if err := ValidatePathFilter(path); err != nil {
			return err
		}
	}
	if len(tokenize(query.Text)) == 0 {
		return fmt.Errorf("search query is empty after tokenization")
	}
	return nil
}

// ValidatePathFilter accepts repository-relative prefixes under managed
// content roots. Repository methods still perform the authoritative symlink
// and escape checks before a search is run.
func ValidatePathFilter(value string) error {
	if value == "" || strings.ContainsRune(value, '\x00') ||
		strings.Contains(value, `\`) || strings.HasPrefix(value, "/") {
		return fmt.Errorf("search path filters must be repository-relative paths under pages/ or sources/")
	}
	clean := strings.TrimSuffix(value, "/")
	parts := strings.Split(clean, "/")
	if len(parts) == 0 || (parts[0] != "pages" && parts[0] != "sources") {
		return fmt.Errorf("search path filters must be under pages/ or sources/")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("search path filters must not contain empty, dot, or traversal segments")
		}
	}
	return nil
}

func matchesTags(documentTags, required []string) bool {
	for _, requiredTag := range required {
		found := false
		for _, documentTag := range documentTags {
			if documentTag == requiredTag {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func matchesPath(documentPath string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, prefix := range prefixes {
		prefix = strings.TrimSuffix(prefix, "/")
		if documentPath == prefix || strings.HasPrefix(documentPath, prefix+"/") {
			return true
		}
	}
	return false
}

func scoreDocument(document *docs.Document, phrase string, queryTokens []string) int {
	title := strings.ToLower(document.Title())
	aliases := lowerValues(document.Aliases())
	tags := lowerValues(document.Tags())
	kindTokens := tokenSet(tokenize(document.Kind()))
	body := strings.ToLower(string(document.Body))
	bodyCounts := tokenCounts(tokenize(body))
	score := 0

	if phrase != "" && strings.Contains(title, phrase) {
		score += 100
	}
	if phrase != "" && anyContains(aliases, phrase) {
		score += 40
	}
	for _, token := range queryTokens {
		if containsToken(title, token) {
			score += 25
		}
		if anyToken(aliases, token) {
			score += 18
		}
		if anyToken(tags, token) {
			score += 8
		}
	}
	if phrase != "" && anyContains(tags, phrase) {
		score += 12
	}
	if phrase != "" && strings.Contains(body, phrase) {
		score += 10
	}
	for _, token := range queryTokens {
		count := bodyCounts[token]
		if count > bodyTokenCap {
			count = bodyTokenCap
		}
		score += count * 3
		if kindTokens[token] {
			score += 2
		}
	}
	return score
}

func bestSnippet(document *docs.Document, phrase string, queryTokens []string) (int, int, string) {
	bodyStart := bytes.Count(document.Data[:document.BodyOffset], []byte{'\n'}) + 1
	return bestSnippetBody(document.Body, bodyStart, phrase, queryTokens)
}

func bestSnippetBody(body []byte, bodyStart int, phrase string, queryTokens []string) (int, int, string) {
	lines := strings.Split(string(body), "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return bodyStart, bodyStart, ""
	}
	best := -1
	if phrase != "" {
		for index, line := range lines {
			if strings.Contains(strings.ToLower(line), phrase) {
				best = index
				break
			}
		}
	}
	if best < 0 {
		bestHits := -1
		for index, line := range lines {
			counts := tokenCounts(tokenize(line))
			hits := 0
			for _, token := range queryTokens {
				hits += counts[token]
			}
			if hits > bestHits {
				best, bestHits = index, hits
			}
		}
	}
	start := best - 1
	if start < 0 {
		start = 0
	}
	end := best + 1
	if end >= len(lines) {
		end = len(lines) - 1
	}
	snippet := strings.Join(lines[start:end+1], "\n")
	snippet = truncateUTF8(snippet, snippetLimit)
	return bodyStart + start, bodyStart + end, snippet
}

func tokenize(value string) []string {
	lower := strings.ToLower(value)
	return strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

func uniqueTokens(tokens []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if !seen[token] {
			seen[token] = true
			out = append(out, token)
		}
	}
	return out
}

func tokenCounts(tokens []string) map[string]int {
	counts := make(map[string]int, len(tokens))
	for _, token := range tokens {
		counts[token]++
	}
	return counts
}

func tokenSet(tokens []string) map[string]bool {
	set := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		set[token] = true
	}
	return set
}

func containsToken(value, token string) bool {
	for _, candidate := range tokenize(value) {
		if candidate == token {
			return true
		}
	}
	return false
}

func anyToken(values []string, token string) bool {
	for _, value := range values {
		if containsToken(value, token) {
			return true
		}
	}
	return false
}

func anyContains(values []string, phrase string) bool {
	for _, value := range values {
		if strings.Contains(value, phrase) {
			return true
		}
	}
	return false
}

func lowerValues(values []string) []string {
	out := make([]string, len(values))
	for index, value := range values {
		out[index] = strings.ToLower(value)
	}
	return out
}

func truncateUTF8(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	end := maximum
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}
