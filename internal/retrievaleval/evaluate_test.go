package retrievaleval

import (
	"reflect"
	"testing"

	"lore/internal/catalog"
	"lore/internal/search"
)

func TestCaseValidationRequiresExplicitAccessAndDisjointExpectations(t *testing.T) {
	valid := Case{
		ID:       "valid_case",
		Category: "direct",
		Query: QuerySpec{
			Text: "needle", Scope: search.ScopePages, Sensitivities: []string{"normal"},
		},
		Relevant: []RelevantSpec{{ID: "page_needle", Grade: 3}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid case: %v", err)
	}

	missingAccess := valid
	missingAccess.Query.Sensitivities = nil
	if err := missingAccess.Validate(); err == nil {
		t.Fatal("case without explicit sensitivities unexpectedly validated")
	}

	overlap := valid
	overlap.ForbiddenIDs = []string{"page_needle"}
	if err := overlap.Validate(); err == nil {
		t.Fatal("case with overlapping relevant and forbidden IDs unexpectedly validated")
	}
}

func TestEvaluateCaseAndMetrics(t *testing.T) {
	testCase := Case{
		ID:       "graded_case",
		Category: "graded",
		Query: QuerySpec{
			Text: "needle", Scope: search.ScopePages, Sensitivities: []string{"normal"}, Limit: 20,
		},
		Relevant: []RelevantSpec{
			{ID: "page_primary", Grade: 3},
			{ID: "page_secondary", Grade: 1},
		},
		ForbiddenIDs: []string{"page_forbidden"},
	}
	response := search.DetailedResponse{
		Backend:    search.BackendIndex,
		IndexState: "fresh",
		Warnings:   []catalog.Warning{{Code: "candidate_limit_reached"}},
		Results: []search.Result{
			{Rank: 1, ID: "page_forbidden", Path: "pages/forbidden.md", Score: 50},
			{
				Rank: 2, ID: "page_primary", Path: "pages/primary.md", Score: 40,
				FuzzyMatches: []search.FuzzyMatch{{QueryTerm: "needel", DocumentTerm: "needle", Distance: 1}},
			},
		},
	}

	result := evaluateCase(testCase, response)
	if result.FirstRelevantRank != 2 || result.ReciprocalRank != 0.5 || result.RecallAt5 != 0.5 {
		t.Fatalf("case metrics = %+v", result)
	}
	if result.NDCGAt10 <= 0 || result.NDCGAt10 >= 1 {
		t.Fatalf("nDCG@10 = %f, want between zero and one", result.NDCGAt10)
	}
	if !reflect.DeepEqual(result.ForbiddenHits, []string{"page_forbidden"}) ||
		!reflect.DeepEqual(result.WarningCodes, []string{"candidate_limit_reached"}) {
		t.Fatalf("case diagnostics = %+v", result)
	}
	if result.Results[0].FuzzyMatches != nil ||
		!reflect.DeepEqual(result.Results[1].FuzzyMatches, []search.FuzzyMatch{{QueryTerm: "needel", DocumentTerm: "needle", Distance: 1}}) {
		t.Fatalf("fuzzy evidence = %+v", result.Results)
	}

	metrics := summarize([]CaseReport{result})
	if metrics.HitAt1 != 0 || metrics.HitAt3 != 1 || metrics.HitAt5 != 1 || metrics.ForbiddenResults != 1 {
		t.Fatalf("summary = %+v", metrics)
	}
}

func TestDiffReportsIdentifiesChangedRanks(t *testing.T) {
	baseline := Report{
		SchemaVersion: ReportSchemaVersion,
		SuiteVersion:  SuiteSchemaVersion,
		Suite:         "test",
		Backends: []BackendReport{{
			RequestedBackend: "auto",
			Overall:          Metrics{Cases: 1},
			Cases: []CaseReport{{
				ID: "case", FirstRelevantRank: 0, Results: []RankedResult{},
			}},
		}},
	}
	current := baseline
	current.Backends = append([]BackendReport(nil), baseline.Backends...)
	current.Backends[0].Cases = append([]CaseReport(nil), baseline.Backends[0].Cases...)
	current.Backends[0].Cases[0].FirstRelevantRank = 1
	if differences := DiffReports(baseline, current); len(differences) == 0 {
		t.Fatal("changed report unexpectedly matched baseline")
	}
	if differences := DiffReports(baseline, baseline); differences != nil {
		t.Fatalf("identical report differences = %v", differences)
	}
}
