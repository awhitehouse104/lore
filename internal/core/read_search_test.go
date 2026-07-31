package core

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"lore/internal/catalog"
	"lore/internal/config"
	"lore/internal/repository"
	"lore/internal/search"
)

func TestParseLineRange(t *testing.T) {
	got, err := ParseLineRange("10:20")
	if err != nil || got != (LineRange{Start: 10, End: 20}) {
		t.Fatalf("ParseLineRange = %+v, %v", got, err)
	}
	for _, value := range []string{"", "1", "1:", ":2", "0:1", "-1:2", "2:1", "a:b", "1:2:3"} {
		if _, err := ParseLineRange(value); err == nil {
			t.Fatalf("ParseLineRange(%q) unexpectedly succeeded", value)
		}
	}
}

func TestSliceLinesAndClamp(t *testing.T) {
	data := []byte("one\ntwo\nthree\n")
	got, start, end, err := sliceLines(data, &LineRange{Start: 2, End: 20})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("two\nthree\n")) || start != 2 || end != 3 {
		t.Fatalf("slice = %q %d:%d", got, start, end)
	}
	all, start, end, err := sliceLines([]byte("no newline"), nil)
	if err != nil || string(all) != "no newline" || start != 1 || end != 1 {
		t.Fatalf("all = %q %d:%d err=%v", all, start, end, err)
	}
	if _, _, _, err := sliceLines(data, &LineRange{Start: 4, End: 5}); err == nil {
		t.Fatal("out-of-bounds start unexpectedly succeeded")
	}
}

type failingSearcher struct{}

func (failingSearcher) Search(context.Context, *repository.Repository, search.Query) ([]search.Result, []catalog.Warning, error) {
	return nil, nil, errors.New("filesystem unavailable")
}

func TestSearchMapsBackendFailureToRuntimeError(t *testing.T) {
	service := &Service{
		Repo:     &repository.Repository{Root: t.TempDir(), Config: config.Defaults()},
		Searcher: failingSearcher{},
	}
	_, err := service.Search(context.Background(), search.Query{Text: "valid query", Access: search.AllAccessPolicy()})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "search_failed" || apiErr.ExitCode != ExitRuntime {
		t.Fatalf("Search error = %T %v", err, err)
	}
}

func TestSearchMapsExplicitFuzzyBreadthToUsageError(t *testing.T) {
	service := &Service{
		Repo:     &repository.Repository{Root: t.TempDir(), Config: config.Defaults()},
		Searcher: failingSearcher{},
	}
	_, err := service.Search(context.Background(), search.Query{
		Text:     "eligibleone eligibletwo eligiblethree eligiblefour eligiblefive eligiblesix eligibleseven eligibleeight eligiblenine",
		Matching: search.MatchingFuzzy,
		Access:   search.AllAccessPolicy(),
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "fuzzy_query_too_broad" || apiErr.ExitCode != ExitUsage {
		t.Fatalf("Search error = %T %v", err, err)
	}
}
