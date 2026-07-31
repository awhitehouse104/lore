package retrievaleval

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"lore/internal/gitx"
	loreindex "lore/internal/index"
	"lore/internal/repository"
	"lore/internal/search"
)

type Report struct {
	SchemaVersion   int             `json:"schema_version"`
	SuiteVersion    int             `json:"suite_version"`
	Suite           string          `json:"suite"`
	Description     string          `json:"description"`
	CorpusDocuments int             `json:"corpus_documents"`
	CaseCount       int             `json:"case_count"`
	Backends        []BackendReport `json:"backends"`
	Parity          ParityReport    `json:"parity"`
}

type BackendReport struct {
	RequestedBackend string           `json:"requested_backend"`
	FilesystemCases  int              `json:"filesystem_cases"`
	IndexedCases     int              `json:"indexed_cases"`
	Overall          Metrics          `json:"overall"`
	Categories       []CategoryReport `json:"categories"`
	Cases            []CaseReport     `json:"cases"`
}

type CategoryReport struct {
	Category string  `json:"category"`
	Metrics  Metrics `json:"metrics"`
}

type Metrics struct {
	Cases              int     `json:"cases"`
	HitAt1             int     `json:"hit_at_1"`
	HitAt3             int     `json:"hit_at_3"`
	HitAt5             int     `json:"hit_at_5"`
	HitAt10            int     `json:"hit_at_10"`
	MeanReciprocalRank float64 `json:"mean_reciprocal_rank"`
	MeanRecallAt5      float64 `json:"mean_recall_at_5"`
	MeanNDCGAt10       float64 `json:"mean_ndcg_at_10"`
	ZeroResults        int     `json:"zero_results"`
	ReturnedResults    int     `json:"returned_results"`
	MeanResults        float64 `json:"mean_results"`
	ForbiddenResults   int     `json:"forbidden_results"`
}

type CaseReport struct {
	ID                string           `json:"id"`
	Category          string           `json:"category"`
	Query             QuerySpec        `json:"query"`
	EffectiveBackend  string           `json:"effective_backend"`
	IndexState        string           `json:"index_state,omitempty"`
	WarningCodes      []string         `json:"warning_codes"`
	Relevant          []RelevantResult `json:"relevant"`
	FirstRelevantRank int              `json:"first_relevant_rank"`
	ReciprocalRank    float64          `json:"reciprocal_rank"`
	RecallAt5         float64          `json:"recall_at_5"`
	NDCGAt10          float64          `json:"ndcg_at_10"`
	ForbiddenHits     []string         `json:"forbidden_hits"`
	Results           []RankedResult   `json:"results"`
}

type RelevantResult struct {
	ID    string `json:"id"`
	Grade int    `json:"grade"`
	Rank  int    `json:"rank"`
}

type RankedResult struct {
	Rank         int                 `json:"rank"`
	ID           string              `json:"id"`
	Path         string              `json:"path"`
	Score        int                 `json:"score"`
	FuzzyMatches []search.FuzzyMatch `json:"fuzzy_matches,omitempty"`
}

type ParityReport struct {
	ComparedCases int              `json:"compared_cases"`
	EqualCases    int              `json:"equal_cases"`
	Mismatches    []ParityMismatch `json:"mismatches"`
}

type ParityMismatch struct {
	CaseID     string         `json:"case_id"`
	Filesystem []RankedResult `json:"filesystem"`
	Auto       []RankedResult `json:"auto"`
}

type RunOptions struct {
	SuitePath   string
	LoreVersion string
}

func Run(ctx context.Context, options RunOptions) (report Report, returnErr error) {
	loaded, err := LoadSuite(options.SuitePath)
	if err != nil {
		return Report{}, err
	}
	stagedRoot, cleanup, err := stageCorpus(loaded.CorpusPath)
	if err != nil {
		return Report{}, err
	}
	defer func() {
		if cleanupErr := cleanup(); cleanupErr != nil && returnErr == nil {
			returnErr = cleanupErr
		}
	}()

	repo, err := repository.Open(stagedRoot)
	if err != nil {
		return Report{}, fmt.Errorf("open staged retrieval corpus: %w", err)
	}
	version := options.LoreVersion
	if version == "" {
		version = "retrieval-eval"
	}
	manager := loreindex.NewManager(repo, evaluationGit{}, version)
	build, err := manager.Build(ctx, loreindex.BuildOptions{})
	if err != nil {
		return Report{}, fmt.Errorf("build retrieval evaluation index: %w", err)
	}
	hybrid := search.HybridSearcher{
		Filesystem:          search.FilesystemLexicalSearcher{},
		Index:               manager,
		CandidateMultiplier: repo.Config.Index.CandidateMultiplier,
		MinimumCandidates:   repo.Config.Index.MinimumCandidates,
		MaximumCandidates:   repo.Config.Index.MaximumCandidates,
	}

	filesystem, filesystemResponses, err := evaluateBackend(ctx, repo, hybrid, loaded.Suite, search.BackendFilesystem)
	if err != nil {
		return Report{}, err
	}
	automatic, automaticResponses, err := evaluateBackend(ctx, repo, hybrid, loaded.Suite, search.BackendAuto)
	if err != nil {
		return Report{}, err
	}
	return Report{
		SchemaVersion:   ReportSchemaVersion,
		SuiteVersion:    loaded.Suite.Version,
		Suite:           loaded.Suite.Name,
		Description:     loaded.Suite.Description,
		CorpusDocuments: build.DocumentCount,
		CaseCount:       len(loaded.Suite.Cases),
		Backends:        []BackendReport{filesystem, automatic},
		Parity:          compareParity(loaded.Suite.Cases, filesystemResponses, automaticResponses),
	}, nil
}

func evaluateBackend(
	ctx context.Context,
	repo *repository.Repository,
	searcher search.HybridSearcher,
	suite Suite,
	backend search.Backend,
) (BackendReport, []search.DetailedResponse, error) {
	report := BackendReport{RequestedBackend: string(backend), Cases: make([]CaseReport, 0, len(suite.Cases))}
	responses := make([]search.DetailedResponse, 0, len(suite.Cases))
	for _, testCase := range suite.Cases {
		if err := ctx.Err(); err != nil {
			return BackendReport{}, nil, err
		}
		query, err := testCase.Query.SearchQuery(backend)
		if err != nil {
			return BackendReport{}, nil, fmt.Errorf("prepare retrieval case %s: %w", testCase.ID, err)
		}
		response, err := searcher.SearchDetailed(ctx, repo, query)
		if err != nil {
			return BackendReport{}, nil, fmt.Errorf("run retrieval case %s with %s backend: %w", testCase.ID, backend, err)
		}
		responses = append(responses, response)
		switch response.Backend {
		case search.BackendFilesystem:
			report.FilesystemCases++
		case search.BackendIndex:
			report.IndexedCases++
		default:
			return BackendReport{}, nil, fmt.Errorf("retrieval case %s returned unknown backend %q", testCase.ID, response.Backend)
		}
		report.Cases = append(report.Cases, evaluateCase(testCase, response))
	}
	report.Overall = summarize(report.Cases)
	report.Categories = summarizeCategories(report.Cases)
	return report, responses, nil
}

func evaluateCase(testCase Case, response search.DetailedResponse) CaseReport {
	ranks := make(map[string]int, len(response.Results))
	results := make([]RankedResult, 0, len(response.Results))
	for _, result := range response.Results {
		ranks[result.ID] = result.Rank
		results = append(results, RankedResult{
			Rank: result.Rank, ID: result.ID, Path: result.Path, Score: result.Score,
			FuzzyMatches: normalizedFuzzyMatches(result.FuzzyMatches),
		})
	}
	relevant := make([]RelevantResult, 0, len(testCase.Relevant))
	firstRank := 0
	foundAt5 := 0
	for _, expected := range testCase.Relevant {
		rank := ranks[expected.ID]
		relevant = append(relevant, RelevantResult{ID: expected.ID, Grade: expected.Grade, Rank: rank})
		if rank > 0 && (firstRank == 0 || rank < firstRank) {
			firstRank = rank
		}
		if rank > 0 && rank <= 5 {
			foundAt5++
		}
	}
	forbiddenSet := make(map[string]struct{}, len(testCase.ForbiddenIDs))
	for _, id := range testCase.ForbiddenIDs {
		forbiddenSet[id] = struct{}{}
	}
	forbiddenHits := make([]string, 0)
	for _, result := range response.Results {
		if _, forbidden := forbiddenSet[result.ID]; forbidden {
			forbiddenHits = append(forbiddenHits, result.ID)
		}
	}
	warningCodes := make([]string, 0, len(response.Warnings))
	for _, warning := range response.Warnings {
		warningCodes = append(warningCodes, warning.Code)
	}
	reciprocalRank := 0.0
	if firstRank > 0 {
		reciprocalRank = 1 / float64(firstRank)
	}
	return CaseReport{
		ID:                testCase.ID,
		Category:          testCase.Category,
		Query:             normalizedQuerySpec(testCase.Query),
		EffectiveBackend:  string(response.Backend),
		IndexState:        response.IndexState,
		WarningCodes:      warningCodes,
		Relevant:          relevant,
		FirstRelevantRank: firstRank,
		ReciprocalRank:    roundMetric(reciprocalRank),
		RecallAt5:         roundMetric(float64(foundAt5) / float64(len(testCase.Relevant))),
		NDCGAt10:          roundMetric(ndcgAt10(relevant)),
		ForbiddenHits:     forbiddenHits,
		Results:           results,
	}
}

func normalizedQuerySpec(spec QuerySpec) QuerySpec {
	if spec.Limit == 0 {
		spec.Limit = defaultResultLimit
	}
	if spec.Matching == "" {
		spec.Matching = search.MatchingAuto
	}
	return spec
}

func summarize(cases []CaseReport) Metrics {
	metrics := Metrics{Cases: len(cases)}
	if len(cases) == 0 {
		return metrics
	}
	var reciprocalRank, recallAt5, ndcg float64
	for _, testCase := range cases {
		rank := testCase.FirstRelevantRank
		if rank > 0 && rank <= 1 {
			metrics.HitAt1++
		}
		if rank > 0 && rank <= 3 {
			metrics.HitAt3++
		}
		if rank > 0 && rank <= 5 {
			metrics.HitAt5++
		}
		if rank > 0 && rank <= 10 {
			metrics.HitAt10++
		}
		if len(testCase.Results) == 0 {
			metrics.ZeroResults++
		}
		metrics.ReturnedResults += len(testCase.Results)
		metrics.ForbiddenResults += len(testCase.ForbiddenHits)
		reciprocalRank += testCase.ReciprocalRank
		recallAt5 += testCase.RecallAt5
		ndcg += testCase.NDCGAt10
	}
	metrics.MeanReciprocalRank = roundMetric(reciprocalRank / float64(len(cases)))
	metrics.MeanRecallAt5 = roundMetric(recallAt5 / float64(len(cases)))
	metrics.MeanNDCGAt10 = roundMetric(ndcg / float64(len(cases)))
	metrics.MeanResults = roundMetric(float64(metrics.ReturnedResults) / float64(len(cases)))
	return metrics
}

func summarizeCategories(cases []CaseReport) []CategoryReport {
	grouped := make(map[string][]CaseReport)
	for _, testCase := range cases {
		grouped[testCase.Category] = append(grouped[testCase.Category], testCase)
	}
	categories := make([]string, 0, len(grouped))
	for category := range grouped {
		categories = append(categories, category)
	}
	sort.Strings(categories)
	reports := make([]CategoryReport, 0, len(categories))
	for _, category := range categories {
		reports = append(reports, CategoryReport{Category: category, Metrics: summarize(grouped[category])})
	}
	return reports
}

func ndcgAt10(relevant []RelevantResult) float64 {
	dcg := 0.0
	grades := make([]int, 0, len(relevant))
	for _, result := range relevant {
		grades = append(grades, result.Grade)
		if result.Rank > 0 && result.Rank <= 10 {
			dcg += discountedGain(result.Grade, result.Rank)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(grades)))
	idcg := 0.0
	for index, grade := range grades {
		if index >= 10 {
			break
		}
		idcg += discountedGain(grade, index+1)
	}
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

func discountedGain(grade, rank int) float64 {
	return (math.Pow(2, float64(grade)) - 1) / math.Log2(float64(rank)+1)
}

func roundMetric(value float64) float64 {
	return math.Round(value*1_000_000) / 1_000_000
}

func compareParity(cases []Case, filesystem, automatic []search.DetailedResponse) ParityReport {
	report := ParityReport{ComparedCases: len(cases), Mismatches: []ParityMismatch{}}
	for index, testCase := range cases {
		if reflect.DeepEqual(filesystem[index].Results, automatic[index].Results) {
			report.EqualCases++
			continue
		}
		report.Mismatches = append(report.Mismatches, ParityMismatch{
			CaseID:     testCase.ID,
			Filesystem: rankedResults(filesystem[index].Results),
			Auto:       rankedResults(automatic[index].Results),
		})
	}
	return report
}

func rankedResults(results []search.Result) []RankedResult {
	ranked := make([]RankedResult, 0, len(results))
	for _, result := range results {
		ranked = append(ranked, RankedResult{
			Rank:         result.Rank,
			ID:           result.ID,
			Path:         result.Path,
			Score:        result.Score,
			FuzzyMatches: normalizedFuzzyMatches(result.FuzzyMatches),
		})
	}
	return ranked
}

func normalizedFuzzyMatches(matches []search.FuzzyMatch) []search.FuzzyMatch {
	if len(matches) == 0 {
		return nil
	}
	return append([]search.FuzzyMatch(nil), matches...)
}

func stageCorpus(source string) (string, func() error, error) {
	temporary, err := os.MkdirTemp("", "lore-retrieval-eval-")
	if err != nil {
		return "", nil, fmt.Errorf("create retrieval evaluation directory: %w", err)
	}
	cleanup := func() error {
		if err := os.RemoveAll(temporary); err != nil {
			return fmt.Errorf("remove retrieval evaluation directory: %w", err)
		}
		return nil
	}
	target := filepath.Join(temporary, "repository")
	if err := os.Mkdir(target, 0o755); err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("create staged retrieval corpus: %w", err)
	}
	if err := os.CopyFS(target, os.DirFS(source)); err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("stage retrieval corpus: %w", err)
	}
	return target, cleanup, nil
}

// evaluationGit gives index evaluation a stable, clean Git-shaped snapshot
// without invoking a developer machine's Git configuration or executable.
type evaluationGit struct{}

func (evaluationGit) IsRepository(context.Context, string) (bool, error) {
	return true, nil
}

func (evaluationGit) HeadOptional(context.Context, string) (string, bool, error) {
	return "1111111111111111111111111111111111111111", true, nil
}

func (evaluationGit) BranchState(context.Context, string) (string, bool, error) {
	return "evaluation", false, nil
}

func (evaluationGit) Changes(context.Context, string, []string) ([]gitx.Change, error) {
	return []gitx.Change{}, nil
}

func (evaluationGit) CommonDirectory(context.Context, string) (string, error) {
	return "/lore/retrieval-evaluation.git", nil
}

func (evaluationGit) RootCommits(context.Context, string) ([]string, error) {
	return []string{"0000000000000000000000000000000000000000"}, nil
}
