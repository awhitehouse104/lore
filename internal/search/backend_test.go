package search

import (
	"context"
	"errors"
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
