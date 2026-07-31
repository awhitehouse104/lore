package search

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lore/internal/repository"
)

func TestBoundedDamerauLevenshtein(t *testing.T) {
	tests := []struct {
		left, right string
		maximum     int
		want        int
	}{
		{left: "disater", right: "disaster", maximum: 2, want: 1},
		{left: "abcd", right: "acbd", maximum: 1, want: 1},
		{left: "identty", right: "identity", maximum: 2, want: 1},
		{left: "recoveries", right: "recovery", maximum: 3, want: 3},
		{left: "cafe", right: "café", maximum: 1, want: 1},
		{left: "short", right: "substantiallylonger", maximum: 2, want: 3},
	}
	for _, test := range tests {
		t.Run(test.left+"/"+test.right, func(t *testing.T) {
			if got := boundedDamerauLevenshtein(test.left, test.right, test.maximum); got != test.want {
				t.Fatalf("distance = %d, want %d", got, test.want)
			}
		})
	}
}

func TestFuzzyPlanningModesAndBounds(t *testing.T) {
	tokens := []string{"project", "projet", "what", "recovry"}
	frequencies := map[string]int{"project": 3}
	automatic, err := PlanFuzzyExpansion(tokens, MatchingAuto, frequencies)
	if err != nil {
		t.Fatal(err)
	}
	if got := targetTerms(automatic); strings.Join(got, ",") != "projet,recovry" {
		t.Fatalf("automatic targets = %v", got)
	}
	lexical, err := PlanFuzzyExpansion(tokens, MatchingLexical, frequencies)
	if err != nil || len(lexical.Targets) != 0 {
		t.Fatalf("lexical plan=%+v err=%v", lexical, err)
	}
	fuzzy, err := PlanFuzzyExpansion(tokens, MatchingFuzzy, frequencies)
	if err != nil {
		t.Fatal(err)
	}
	if got := targetTerms(fuzzy); strings.Join(got, ",") != "project,projet,what,recovry" {
		t.Fatalf("fuzzy targets = %v", got)
	}

	many := make([]string, MaximumFuzzyQueryTokens+1)
	for index := range many {
		many[index] = fmt.Sprintf("eligibleterm%d", index)
	}
	if _, err := PlanFuzzyExpansion(many, MatchingFuzzy, nil); err == nil {
		t.Fatal("explicit fuzzy plan unexpectedly accepted too many eligible terms")
	}
	truncated, err := PlanFuzzyExpansion(many, MatchingAuto, nil)
	if err != nil || !truncated.Truncated || len(truncated.Targets) != MaximumFuzzyQueryTokens {
		t.Fatalf("automatic broad plan=%+v err=%v", truncated, err)
	}
}

func TestFuzzyExpansionSelectionIsDeterministicAndBounded(t *testing.T) {
	plan := FuzzyPlan{Targets: []FuzzyTarget{{Term: "stone", RuneLength: 5, MaxDistance: 1}}}
	vocabulary := []VocabularyTerm{
		{Term: "stoke", DocumentFrequency: 1},
		{Term: "store", DocumentFrequency: 4},
		{Term: "stony", DocumentFrequency: 2},
		{Term: "atone", DocumentFrequency: 5},
		{Term: "shone", DocumentFrequency: 3},
		{Term: "unrelated", DocumentFrequency: 100},
	}
	expansions := SelectFuzzyExpansions(plan, vocabulary)
	if len(expansions) != MaximumFuzzyExpansions {
		t.Fatalf("expansion count = %d", len(expansions))
	}
	got := make([]string, len(expansions))
	for index, expansion := range expansions {
		got[index] = expansion.DocumentTerm
	}
	if strings.Join(got, ",") != "atone,store,shone,stony" {
		t.Fatalf("expansions = %v", got)
	}
	expression := ExtendMatchExpression(`"stone"*`, expansions)
	if expression != `"stone"* OR "atone" OR "shone" OR "stony" OR "store"` {
		t.Fatalf("expression = %q", expression)
	}
}

func TestFuzzyExpansionRejectsLowSimilarityDistanceTwo(t *testing.T) {
	plan := FuzzyPlan{Targets: []FuzzyTarget{{Term: "preserve", RuneLength: 8, MaxDistance: 2}}}
	if expansions := SelectFuzzyExpansions(plan, []VocabularyTerm{{Term: "deserve", DocumentFrequency: 1}}); len(expansions) != 0 {
		t.Fatalf("low-similarity expansions = %+v", expansions)
	}
	plan = FuzzyPlan{Targets: []FuzzyTarget{{Term: "recoveries", RuneLength: 10, MaxDistance: 2}}}
	if expansions := SelectFuzzyExpansions(plan, []VocabularyTerm{{Term: "recovered", DocumentFrequency: 1}}); len(expansions) != 1 {
		t.Fatalf("80-percent expansion = %+v", expansions)
	}
}

func TestFilesystemAdaptiveFuzzyMatchingAndEvidence(t *testing.T) {
	repo := searchRepository(t)
	writePage(t, repo, "pages/recovery.md", "page_recovery", "Disaster Recovery", "Restore service after failure.")
	searcher := FilesystemLexicalSearcher{}
	query := Query{
		Text: "disater recovry", Scope: ScopePages, Limit: 10,
		Matching: MatchingAuto, Access: AllAccessPolicy(),
	}
	results, _, err := searcher.Search(context.Background(), repo, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "page_recovery" || len(results[0].FuzzyMatches) != 2 {
		t.Fatalf("automatic fuzzy results = %+v", results)
	}
	if results[0].FuzzyMatches[0].QueryTerm != "disater" || results[0].FuzzyMatches[0].DocumentTerm != "disaster" ||
		results[0].FuzzyMatches[1].QueryTerm != "recovry" || results[0].FuzzyMatches[1].DocumentTerm != "recovery" {
		t.Fatalf("fuzzy evidence = %+v", results[0].FuzzyMatches)
	}
	query.Matching = MatchingLexical
	results, _, err = searcher.Search(context.Background(), repo, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("lexical typo results = %+v", results)
	}
}

func TestFilesystemFuzzyVocabularyRespectsAccess(t *testing.T) {
	repo := searchRepository(t)
	writePageWithSensitivity(t, repo, "pages/private.md", "page_private", "Private", "sensitive", "heliotrope plans")
	writePageWithSensitivity(t, repo, "pages/public.md", "page_public", "Public", "normal", "ordinary notes")
	searcher := FilesystemLexicalSearcher{}
	normal, err := NewAccessPolicy([]string{"normal"})
	if err != nil {
		t.Fatal(err)
	}
	query := Query{Text: "heliotropd", Scope: ScopePages, Limit: 10, Matching: MatchingAuto, Access: normal}
	results, _, err := searcher.Search(context.Background(), repo, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("normal-only fuzzy search exposed private vocabulary: %+v", results)
	}
	query.Access = AllAccessPolicy()
	results, _, err = searcher.Search(context.Background(), repo, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "page_private" {
		t.Fatalf("all-access fuzzy results = %+v", results)
	}
}

func TestExplicitFuzzyExpandsKnownShortTerm(t *testing.T) {
	repo := searchRepository(t)
	writePage(t, repo, "pages/form.md", "page_form", "Form", "form")
	writePage(t, repo, "pages/from.md", "page_from", "From", "from")
	searcher := FilesystemLexicalSearcher{}
	query := Query{Text: "form", Scope: ScopePages, Limit: 10, Matching: MatchingAuto, Access: AllAccessPolicy()}
	automatic, _, err := searcher.Search(context.Background(), repo, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(automatic) != 1 || automatic[0].ID != "page_form" {
		t.Fatalf("automatic known-term results = %+v", automatic)
	}
	query.Matching = MatchingFuzzy
	explicit, _, err := searcher.Search(context.Background(), repo, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(explicit) != 2 || explicit[0].ID != "page_form" || explicit[1].ID != "page_from" || len(explicit[1].FuzzyMatches) != 1 {
		t.Fatalf("explicit fuzzy results = %+v", explicit)
	}
}

func TestFuzzyVocabularyLimitNeverReturnsPartialExpansion(t *testing.T) {
	candidates := []Candidate{{
		Title: "Recovery", Kind: "reference", Body: []byte("restore restory"),
	}}
	tokens := []string{"recovry"}
	_, expansions, warnings, err := prepareFuzzyExpansionWithLimit(candidates, tokens, MatchingAuto, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(expansions) != 0 || len(warnings) != 1 || warnings[0].Code != "fuzzy_vocabulary_limit" {
		t.Fatalf("automatic limited expansion=%+v warnings=%+v", expansions, warnings)
	}
	_, expansions, warnings, err = prepareFuzzyExpansionWithLimit(candidates, tokens, MatchingFuzzy, 1)
	var matchingErr *MatchingError
	if !errors.As(err, &matchingErr) || matchingErr.Code != "fuzzy_vocabulary_limit" || expansions != nil || warnings != nil {
		t.Fatalf("explicit limited expansion=%+v warnings=%+v err=%T %v", expansions, warnings, err, err)
	}
}

func targetTerms(plan FuzzyPlan) []string {
	terms := make([]string, len(plan.Targets))
	for index, target := range plan.Targets {
		terms[index] = target.Term
	}
	return terms
}

func BenchmarkSelectFuzzyExpansions100K(b *testing.B) {
	vocabulary := make([]VocabularyTerm, 100_000)
	for index := range vocabulary {
		vocabulary[index] = VocabularyTerm{
			Term: fmt.Sprintf("term%06d", index), DocumentFrequency: index%17 + 1,
		}
	}
	plan := FuzzyPlan{Targets: []FuzzyTarget{
		{Term: "term00001x", Position: 0, RuneLength: 10, MaxDistance: 2},
		{Term: "term9999x9", Position: 1, RuneLength: 10, MaxDistance: 2},
	}}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if expansions := SelectFuzzyExpansions(plan, vocabulary); len(expansions) == 0 {
			b.Fatal("expected fuzzy expansions")
		}
	}
}

func writePageWithSensitivity(t *testing.T, repo *repository.Repository, path, id, title, sensitivity, body string) {
	t.Helper()
	data := []byte("---\nid: " + id + "\ntitle: " + title + "\nkind: topic\ncreated: \"2026-07-31\"\nupdated: \"2026-07-31\"\nstatus: active\nsensitivity: " + sensitivity + "\n---\n" + body)
	if err := os.WriteFile(filepath.Join(repo.Root, filepath.FromSlash(path)), data, 0o644); err != nil {
		t.Fatal(err)
	}
}
