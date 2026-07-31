package search

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"lore/internal/docs"
	"lore/internal/repository"
)

type fakeIndexedBackend struct {
	status         IndexedStatus
	statusErr      error
	batch          CandidateBatch
	candidateErr   error
	statusCalls    int
	candidateCalls int
	request        CandidateRequest
}

func (f *fakeIndexedBackend) IndexSearchStatus(context.Context, bool) (IndexedStatus, error) {
	f.statusCalls++
	return f.status, f.statusErr
}

func (f *fakeIndexedBackend) IndexCandidates(_ context.Context, request CandidateRequest) (CandidateBatch, error) {
	f.candidateCalls++
	f.request = request
	return f.batch, f.candidateErr
}

func TestBuildMatchExpressionTreatsControlSyntaxAsText(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{query: `foo:bar`, want: `"foo" OR "bar"*`},
		{query: `NEAR(foo bar)`, want: `"near" OR "foo" OR "bar"*`},
		{query: `"unterminated`, want: `"unterminated"*`},
		{query: `../../secrets`, want: `"secrets"*`},
		{query: `select drop table`, want: `"select" OR "drop" OR "table"*`},
		{query: `café 東京`, want: `"café" OR "東京"`},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			got, err := BuildMatchExpression(tokenize(test.query))
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("expression = %q, want %q", got, test.want)
			}
			for _, raw := range []string{":", "(", ")", "../", "NEAR("} {
				if strings.Contains(got, raw) {
					t.Fatalf("expression retained control syntax %q: %q", raw, got)
				}
			}
		})
	}
}

func TestCandidateLimit(t *testing.T) {
	tests := []struct {
		limit, multiplier, minimum, maximum int
		want                                int
	}{
		{limit: 10, multiplier: 20, minimum: 200, maximum: 2000, want: 200},
		{limit: 50, multiplier: 20, minimum: 200, maximum: 2000, want: 1000},
		{limit: 100, multiplier: 30, minimum: 200, maximum: 2000, want: 2000},
		{limit: 1, multiplier: 2, minimum: 10, maximum: 100, want: 10},
	}
	for _, test := range tests {
		if got := CandidateLimit(test.limit, test.multiplier, test.minimum, test.maximum); got != test.want {
			t.Fatalf("CandidateLimit(%+v) = %d", test, got)
		}
	}
}

func TestLexicalRankingRewardsCoverageAndRarity(t *testing.T) {
	candidates := []Candidate{
		{
			Path: "pages/repeated.md", DocumentID: "page_repeated", DocumentType: docs.TypePage,
			Title: "Repeated", Kind: "note", Sensitivity: "normal",
			Body: []byte("alpha alpha alpha alpha alpha alpha alpha alpha alpha alpha"), BodyLineStart: 10,
		},
		{
			Path: "pages/covered.md", DocumentID: "page_covered", DocumentType: docs.TypePage,
			Title: "Covered", Kind: "note", Sensitivity: "normal",
			Body: []byte("alpha has a gap before beta"), BodyLineStart: 10,
		},
	}
	results := RankCandidates(candidates, Query{
		Text: "alpha beta", Scope: ScopePages, Limit: 10, Access: AllAccessPolicy(),
	})
	if len(results) != 2 || results[0].ID != "page_covered" {
		t.Fatalf("coverage did not outrank repeated single-term use: %+v", results)
	}

	rarityCandidates := []Candidate{
		{
			Path: "pages/common.md", DocumentID: "page_common", DocumentType: docs.TypePage,
			Title: "Common", Kind: "note", Sensitivity: "normal", Body: []byte("common"), BodyLineStart: 10,
		},
		{
			Path: "pages/rare.md", DocumentID: "page_rare", DocumentType: docs.TypePage,
			Title: "Rare", Kind: "note", Sensitivity: "normal", Body: []byte("zephyr"), BodyLineStart: 10,
		},
		{
			Path: "pages/common-two.md", DocumentID: "page_common_two", DocumentType: docs.TypePage,
			Title: "Other", Kind: "note", Sensitivity: "normal", Body: []byte("common"), BodyLineStart: 10,
		},
		{
			Path: "pages/common-three.md", DocumentID: "page_common_three", DocumentType: docs.TypePage,
			Title: "Another", Kind: "note", Sensitivity: "normal", Body: []byte("common"), BodyLineStart: 10,
		},
	}
	rarityResults := RankCandidates(rarityCandidates, Query{
		Text: "common zephyr", Scope: ScopePages, Limit: 10, Access: AllAccessPolicy(),
	})
	var commonScore, rareScore int
	for _, result := range rarityResults {
		switch result.ID {
		case "page_common_two":
			commonScore = result.Score
		case "page_rare":
			rareScore = result.Score
		}
	}
	if rareScore <= commonScore {
		t.Fatalf("rare exact token score %d did not exceed common exact token score %d", rareScore, commonScore)
	}
}

func TestLexicalRankingScoresSeparateTagTokens(t *testing.T) {
	results := RankCandidates([]Candidate{{
		Path: "pages/tags.md", DocumentID: "page_tags", DocumentType: docs.TypePage,
		Title: "Tags", Kind: "note", Sensitivity: "normal",
		Tags: []string{"security", "deployment"}, BodyLineStart: 10,
	}}, Query{
		Text: "security deployment", Scope: ScopePages, Limit: 10, Access: AllAccessPolicy(),
	})
	if len(results) != 1 || results[0].Score != 44 {
		t.Fatalf("separate tag token score = %+v, want 44", results)
	}
}

func TestKnownCorpusFrequencyKeepsBoundedCandidateScoresStable(t *testing.T) {
	candidates := make([]Candidate, 20)
	for index := range candidates {
		candidates[index] = Candidate{
			Path: fmt.Sprintf("pages/common-%02d.md", index), DocumentID: fmt.Sprintf("page_common_%02d", index),
			DocumentType: docs.TypePage, Title: "Common", Kind: "note", Sensitivity: "normal",
			Body: []byte("common evidence"), BodyLineStart: 10,
		}
	}
	query := Query{Text: "common", Scope: ScopePages, Limit: 10, Access: AllAccessPolicy()}
	bounded := rankCandidates(candidates, query, 60, map[string]int{"common": 60}, nil)
	uncorrected := rankCandidates(candidates, query, 60, nil, nil)
	if len(bounded) == 0 || len(uncorrected) == 0 || bounded[0].Score != 142 || uncorrected[0].Score <= bounded[0].Score {
		t.Fatalf("bounded score=%+v uncorrected=%+v", bounded, uncorrected)
	}
}

func TestHybridBackendSelectionFallbackAndExplicitRefusal(t *testing.T) {
	repo := searchRepository(t)
	writePage(t, repo, "pages/evidence.md", "page_evidence", "Evidence", "ordinary evidence")
	access := AllAccessPolicy()

	missing := &fakeIndexedBackend{status: IndexedStatus{State: "missing"}}
	hybrid := testHybrid(missing)
	response, err := hybrid.SearchDetailed(context.Background(), repo, Query{
		Text: "ordinary", Scope: ScopePages, Limit: 10, Backend: BackendAuto, Access: access,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Backend != BackendFilesystem || response.IndexState != "missing" || len(response.Warnings) == 0 {
		t.Fatalf("auto missing response = %+v", response)
	}
	_, err = hybrid.SearchDetailed(context.Background(), repo, Query{
		Text: "ordinary", Scope: ScopePages, Limit: 10, Backend: BackendIndex, Access: access,
	})
	var backendErr *BackendError
	if !errors.As(err, &backendErr) || backendErr.Kind != BackendErrorRuntime {
		t.Fatalf("explicit missing error = %T %v", err, err)
	}

	short := &fakeIndexedBackend{status: IndexedStatus{State: "fresh"}}
	response, err = testHybrid(short).SearchDetailed(context.Background(), repo, Query{
		Text: "x", Scope: ScopePages, Limit: 10, Backend: BackendAuto, Access: access,
	})
	if err != nil || response.Backend != BackendFilesystem || short.statusCalls != 0 {
		t.Fatalf("short auto response=%+v err=%v status_calls=%d", response, err, short.statusCalls)
	}
	_, err = testHybrid(short).SearchDetailed(context.Background(), repo, Query{
		Text: "x", Scope: ScopePages, Limit: 10, Backend: BackendIndex, Access: access,
	})
	if !errors.As(err, &backendErr) || backendErr.Code != "index_query_unsuitable" || short.statusCalls != 0 {
		t.Fatalf("short explicit error=%T %v status_calls=%d", err, err, short.statusCalls)
	}

	filesystemOnly := &fakeIndexedBackend{statusErr: errors.New("must not open")}
	response, err = testHybrid(filesystemOnly).SearchDetailed(context.Background(), repo, Query{
		Text: "ordinary", Scope: ScopePages, Limit: 10, Backend: BackendFilesystem, Access: access,
	})
	if err != nil || response.Backend != BackendFilesystem || filesystemOnly.statusCalls != 0 {
		t.Fatalf("filesystem response=%+v err=%v status_calls=%d", response, err, filesystemOnly.statusCalls)
	}
}

func TestHybridCandidateRankingSensitivityAndBoundWarning(t *testing.T) {
	indexed := &fakeIndexedBackend{
		status: IndexedStatus{State: "fresh"},
		batch: CandidateBatch{
			LimitReached: true,
			Documents: []Candidate{
				{
					Path: "pages/sensitive.md", DocumentID: "page_sensitive", DocumentType: docs.TypePage,
					Title: "Private Needle", Kind: "note", Sensitivity: "sensitive",
					Body: []byte("needle"), BodyLineStart: 10, Revision: "sha256:sensitive",
				},
				{
					Path: "pages/normal.md", DocumentID: "page_normal", DocumentType: docs.TypePage,
					Title: "Needle", Kind: "note", Sensitivity: "normal",
					Body: []byte("needle"), BodyLineStart: 10, Revision: "sha256:normal",
				},
			},
		},
	}
	access, err := NewAccessPolicy([]string{"normal"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := testHybrid(indexed).SearchDetailed(context.Background(), &repository.Repository{}, Query{
		Text: "needle", Scope: ScopePages, Limit: 10, Backend: BackendIndex, Access: access,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Backend != BackendIndex || len(response.Results) != 1 || response.Results[0].ID != "page_normal" {
		t.Fatalf("response = %+v", response)
	}
	if len(response.Warnings) != 1 || response.Warnings[0].Code != "candidate_limit_reached" {
		t.Fatalf("warnings = %+v", response.Warnings)
	}
	if len(indexed.request.AllowedSensitivities) != 1 || indexed.request.AllowedSensitivities[0] != "normal" {
		t.Fatalf("candidate access = %v", indexed.request.AllowedSensitivities)
	}
	if indexed.request.Matching != MatchingAuto {
		t.Fatalf("candidate matching = %q", indexed.request.Matching)
	}
}

func testHybrid(index IndexedBackend) HybridSearcher {
	return HybridSearcher{
		Filesystem:          FilesystemLexicalSearcher{},
		Index:               index,
		CandidateMultiplier: 20,
		MinimumCandidates:   200,
		MaximumCandidates:   2000,
	}
}
