package search

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"lore/internal/catalog"
	"lore/internal/docs"
	"lore/internal/repository"
)

type Backend string

const (
	BackendAuto       Backend = "auto"
	BackendIndex      Backend = "index"
	BackendFilesystem Backend = "filesystem"
)

var allSensitivities = []string{"local-only", "normal", "sensitive"}

type AccessPolicy struct {
	AllowedSensitivities map[string]struct{}
}

func AllAccessPolicy() AccessPolicy {
	allowed := make(map[string]struct{}, len(allSensitivities))
	for _, sensitivity := range allSensitivities {
		allowed[sensitivity] = struct{}{}
	}
	return AccessPolicy{AllowedSensitivities: allowed}
}

func NewAccessPolicy(values []string) (AccessPolicy, error) {
	allowed := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !docs.ValidSensitivity(value) {
			return AccessPolicy{}, fmt.Errorf("included sensitivity must be normal, sensitive, or local-only")
		}
		allowed[value] = struct{}{}
	}
	return AccessPolicy{AllowedSensitivities: allowed}, nil
}

func (p AccessPolicy) Validate() error {
	for sensitivity := range p.AllowedSensitivities {
		if !docs.ValidSensitivity(sensitivity) {
			return fmt.Errorf("included sensitivity must be normal, sensitive, or local-only")
		}
	}
	return nil
}

func (p AccessPolicy) Allows(sensitivity string) bool {
	if p.AllowedSensitivities == nil {
		return true
	}
	_, allowed := p.AllowedSensitivities[sensitivity]
	return allowed
}

func (p AccessPolicy) Values() []string {
	values := make([]string, 0, len(p.AllowedSensitivities))
	for value := range p.AllowedSensitivities {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

type DetailedResponse struct {
	Backend          Backend
	BackendRequested Backend
	IndexState       string
	Results          []Result
	Warnings         []catalog.Warning
}

type DetailedSearcher interface {
	SearchDetailed(context.Context, *repository.Repository, Query) (DetailedResponse, error)
}

type IndexedStatus struct {
	State           string
	ManifestMatches bool
}

type Candidate struct {
	Path          string
	DocumentID    string
	DocumentType  docs.Type
	Title         string
	Kind          string
	Sensitivity   string
	Aliases       []string
	Tags          []string
	Body          []byte
	BodyLineStart int
	Revision      string
}

type CandidateRequest struct {
	MatchExpression      string
	Scope                Scope
	Kind                 string
	Tags                 []string
	Paths                []string
	AllowedSensitivities []string
	Limit                int
}

type CandidateBatch struct {
	Documents    []Candidate
	LimitReached bool
}

type IndexedBackend interface {
	IndexSearchStatus(context.Context, bool) (IndexedStatus, error)
	IndexCandidates(context.Context, CandidateRequest) (CandidateBatch, error)
}

type HybridSearcher struct {
	Filesystem          FilesystemLexicalSearcher
	Index               IndexedBackend
	CandidateMultiplier int
	MinimumCandidates   int
	MaximumCandidates   int
}

func (h HybridSearcher) Search(ctx context.Context, repo *repository.Repository, query Query) ([]Result, []catalog.Warning, error) {
	response, err := h.SearchDetailed(ctx, repo, query)
	return response.Results, response.Warnings, err
}

func (h HybridSearcher) SearchDetailed(ctx context.Context, repo *repository.Repository, query Query) (DetailedResponse, error) {
	if err := ValidateQuery(query); err != nil {
		return DetailedResponse{}, err
	}
	requested := query.Backend
	if requested == "" {
		requested = BackendAuto
	}
	response := DetailedResponse{
		BackendRequested: requested,
		IndexState:       "",
		Warnings:         []catalog.Warning{},
	}
	if requested == BackendFilesystem {
		return h.filesystem(ctx, repo, query, response)
	}
	tokens := uniqueTokens(tokenize(query.Text))
	if len(tokens) > MaximumIndexQueryTokens {
		if requested == BackendAuto {
			response.Warnings = append(response.Warnings, catalog.Warning{
				Code:    "index_query_fallback",
				Path:    ".lore/index.sqlite",
				Message: "query has too many terms for bounded indexed candidate generation",
			})
			return h.filesystem(ctx, repo, query, response)
		}
		return response, &BackendError{
			Kind:    BackendErrorUsage,
			Code:    "index_query_unsuitable",
			Message: fmt.Sprintf("explicit index search supports at most %d unique query terms", MaximumIndexQueryTokens),
		}
	}
	suitable := SuitableForAutomaticIndex(tokens)
	if requested == BackendAuto && !suitable {
		response.Warnings = append(response.Warnings, catalog.Warning{
			Code:    "index_query_fallback",
			Path:    ".lore/index.sqlite",
			Message: "query shape uses filesystem search to preserve lexical parity",
		})
		return h.filesystem(ctx, repo, query, response)
	}
	if requested == BackendIndex && !suitable {
		return response, &BackendError{
			Kind:    BackendErrorUsage,
			Code:    "index_query_unsuitable",
			Message: "explicit index search requires query terms of at least three Unicode letters or digits",
		}
	}
	if h.Index == nil {
		if requested == BackendAuto {
			response.IndexState = "missing"
			response.Warnings = append(response.Warnings, fallbackWarning("missing"))
			return h.filesystem(ctx, repo, query, response)
		}
		return response, unavailableIndexError("missing")
	}
	verifyManifest := requested == BackendIndex
	indexStatus, err := h.Index.IndexSearchStatus(ctx, verifyManifest)
	if err != nil {
		if ctx.Err() != nil {
			return response, ctx.Err()
		}
		if requested == BackendAuto {
			response.Warnings = append(response.Warnings, catalog.Warning{
				Code:    "index_fallback",
				Path:    ".lore/index.sqlite",
				Message: "index status could not be verified; filesystem search was used",
			})
			return h.filesystem(ctx, repo, query, response)
		}
		return response, &BackendError{Kind: BackendErrorRuntime, Code: "index_status_failed", Message: "explicit index status could not be verified", Cause: err}
	}
	response.IndexState = indexStatus.State
	usable := indexStatus.State == "fresh" ||
		(indexStatus.State == "uncertified" && requested == BackendIndex && indexStatus.ManifestMatches)
	if !usable {
		if requested == BackendAuto {
			response.Warnings = append(response.Warnings, fallbackWarning(indexStatus.State))
			return h.filesystem(ctx, repo, query, response)
		}
		return response, unavailableIndexError(indexStatus.State)
	}
	expression, err := BuildMatchExpression(tokens)
	if err != nil {
		return response, err
	}
	candidateLimit := CandidateLimit(
		query.Limit,
		h.CandidateMultiplier,
		h.MinimumCandidates,
		h.MaximumCandidates,
	)
	batch, err := h.Index.IndexCandidates(ctx, CandidateRequest{
		MatchExpression:      expression,
		Scope:                query.Scope,
		Kind:                 query.Kind,
		Tags:                 query.Tags,
		Paths:                query.Paths,
		AllowedSensitivities: query.Access.Values(),
		Limit:                candidateLimit,
	})
	if err != nil {
		if ctx.Err() != nil {
			return response, ctx.Err()
		}
		if requested == BackendAuto {
			response.Warnings = append(response.Warnings, catalog.Warning{
				Code:    "index_fallback",
				Path:    ".lore/index.sqlite",
				Message: "indexed candidate generation failed; filesystem search was used",
			})
			return h.filesystem(ctx, repo, query, response)
		}
		return response, &BackendError{Kind: BackendErrorRuntime, Code: "index_search_failed", Message: "indexed candidate generation failed", Cause: err}
	}
	response.Backend = BackendIndex
	response.Results = RankCandidates(batch.Documents, query)
	if batch.LimitReached {
		response.Warnings = append(response.Warnings, catalog.Warning{
			Code:    "candidate_limit_reached",
			Path:    ".lore/index.sqlite",
			Message: fmt.Sprintf("indexed candidate generation reached its bound of %d documents", candidateLimit),
		})
	}
	return response, nil
}

func (h HybridSearcher) filesystem(
	ctx context.Context,
	repo *repository.Repository,
	query Query,
	response DetailedResponse,
) (DetailedResponse, error) {
	query.Backend = BackendFilesystem
	results, warnings, err := h.Filesystem.Search(ctx, repo, query)
	if err != nil {
		return response, err
	}
	response.Backend = BackendFilesystem
	response.Results = results
	response.Warnings = append(response.Warnings, warnings...)
	return response, nil
}

const MaximumIndexQueryTokens = 64

func SuitableForAutomaticIndex(tokens []string) bool {
	if len(tokens) == 0 || len(tokens) > MaximumIndexQueryTokens {
		return false
	}
	for _, token := range tokens {
		if utf8.RuneCountInString(token) < 3 {
			return false
		}
	}
	return true
}

func BuildMatchExpression(tokens []string) (string, error) {
	tokens = uniqueTokens(tokens)
	if len(tokens) == 0 {
		return "", fmt.Errorf("index query has no searchable tokens")
	}
	if len(tokens) > MaximumIndexQueryTokens {
		return "", fmt.Errorf("index query has too many searchable tokens")
	}
	parts := make([]string, len(tokens))
	for index, token := range tokens {
		quoted := `"` + strings.ReplaceAll(token, `"`, `""`) + `"`
		if index == len(tokens)-1 && utf8.RuneCountInString(token) >= 3 {
			quoted += "*"
		}
		parts[index] = quoted
	}
	return strings.Join(parts, " OR "), nil
}

func CandidateLimit(limit, multiplier, minimum, maximum int) int {
	if limit <= 0 {
		limit = DefaultLimit
	}
	if multiplier <= 0 {
		multiplier = 20
	}
	if minimum <= 0 {
		minimum = 200
	}
	if maximum <= 0 {
		maximum = 2000
	}
	candidates := limit * multiplier
	if candidates < minimum {
		candidates = minimum
	}
	if candidates > maximum {
		candidates = maximum
	}
	return candidates
}

type BackendErrorKind string

const (
	BackendErrorUsage    BackendErrorKind = "usage"
	BackendErrorRuntime  BackendErrorKind = "runtime"
	BackendErrorConflict BackendErrorKind = "conflict"
)

type BackendError struct {
	Kind    BackendErrorKind
	Code    string
	State   string
	Message string
	Cause   error
}

func (e *BackendError) Error() string {
	if e.Cause == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.Cause)
}

func (e *BackendError) Unwrap() error {
	return e.Cause
}

func unavailableIndexError(state string) *BackendError {
	kind := BackendErrorRuntime
	code := "index_unavailable"
	message := fmt.Sprintf("explicit index search requires a fresh compatible index; current state is %s", state)
	if state == "stale" || state == "building" || state == "uncertified" {
		kind = BackendErrorConflict
		code = "index_not_fresh"
	}
	return &BackendError{Kind: kind, Code: code, State: state, Message: message}
}

func fallbackWarning(state string) catalog.Warning {
	return catalog.Warning{
		Code:    "index_fallback",
		Path:    ".lore/index.sqlite",
		Message: fmt.Sprintf("index state is %s; filesystem search was used", state),
	}
}

func RankCandidates(candidates []Candidate, query Query) []Result {
	if query.Limit == 0 {
		query.Limit = DefaultLimit
	}
	tokens := uniqueTokens(tokenize(query.Text))
	phrase := strings.ToLower(strings.TrimSpace(query.Text))
	results := make([]Result, 0, len(candidates))
	for _, candidate := range candidates {
		if !query.Access.Allows(candidate.Sensitivity) {
			continue
		}
		if !matchesTags(candidate.Tags, query.Tags) || !matchesPath(candidate.Path, query.Paths) {
			continue
		}
		document := candidateDocument(candidate)
		score := scoreDocument(document, phrase, tokens)
		if score == 0 {
			continue
		}
		lineStart, lineEnd, snippet := bestSnippetBody(candidate.Body, candidate.BodyLineStart, phrase, tokens)
		results = append(results, Result{
			Score:       score,
			Path:        candidate.Path,
			URI:         fmt.Sprintf("lore://%s#L%d-L%d", candidate.Path, lineStart, lineEnd),
			ResourceURI: resourceURI(candidate.DocumentType, candidate.DocumentID),
			ID:          candidate.DocumentID,
			Title:       candidate.Title,
			Kind:        candidate.Kind,
			LineStart:   lineStart,
			LineEnd:     lineEnd,
			Snippet:     snippet,
			Revision:    candidate.Revision,
		})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Path < results[j].Path
	})
	if len(results) > query.Limit {
		results = results[:query.Limit]
	}
	for index := range results {
		results[index].Rank = index + 1
	}
	return results
}

func candidateDocument(candidate Candidate) *docs.Document {
	document := &docs.Document{
		Path: candidate.Path,
		Type: candidate.DocumentType,
		Body: candidate.Body,
	}
	if candidate.DocumentType == docs.TypePage {
		document.Page = &docs.Page{
			ID:          candidate.DocumentID,
			Title:       candidate.Title,
			Kind:        candidate.Kind,
			Sensitivity: candidate.Sensitivity,
			Aliases:     candidate.Aliases,
			Tags:        candidate.Tags,
		}
	} else {
		document.Source = &docs.Source{
			ID:          candidate.DocumentID,
			Kind:        candidate.Kind,
			Sensitivity: candidate.Sensitivity,
			Tags:        candidate.Tags,
		}
	}
	return document
}
