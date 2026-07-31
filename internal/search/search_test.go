package search

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"lore/internal/config"
	"lore/internal/docs"
	"lore/internal/repository"
)

func TestScoringComponents(t *testing.T) {
	tests := []struct {
		name   string
		doc    *docs.Document
		phrase string
		tokens []string
		want   int
	}{
		{
			name: "title phrase and tokens",
			doc: &docs.Document{Type: docs.TypePage, Page: &docs.Page{
				Title: "Project Foo",
			}},
			phrase: "project foo",
			tokens: []string{"project", "foo"},
			want:   150,
		},
		{
			name: "alias phrase and tokens",
			doc: &docs.Document{Type: docs.TypePage, Page: &docs.Page{
				Aliases: []string{"Project Foo"},
			}},
			phrase: "project foo",
			tokens: []string{"project", "foo"},
			want:   76,
		},
		{
			name: "tag phrase",
			doc: &docs.Document{Type: docs.TypePage, Page: &docs.Page{
				Tags: []string{"project foo"},
			}},
			phrase: "project foo",
			tokens: []string{"project", "foo"},
			want:   28,
		},
		{
			name:   "body phrase and occurrences",
			doc:    &docs.Document{Type: docs.TypePage, Body: []byte("Project Foo project"), Page: &docs.Page{}},
			phrase: "project foo",
			tokens: []string{"project", "foo"},
			want:   19,
		},
		{
			name: "kind token",
			doc: &docs.Document{Type: docs.TypePage, Page: &docs.Page{
				Kind: "project",
			}},
			phrase: "project",
			tokens: []string{"project"},
			want:   2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scoreDocument(tt.doc, tt.phrase, tt.tokens); got != tt.want {
				t.Fatalf("score = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestFilesystemSearchRankingTieBreakAndSnippet(t *testing.T) {
	repo := searchRepository(t)
	writePage(t, repo, "pages/a-title.md", "page_title", "Project Foo", "Nothing in the body.")
	writePage(t, repo, "pages/b-phrase.md", "page_phrase", "Other", "Before.\nProject Foo is deployable.\nAfter.")
	writePage(t, repo, "pages/c-disconnected.md", "page_disconnected", "Other", "Project details here. Foo appears later.")
	writePage(t, repo, "pages/d-tie.md", "page_tie_d", "Tie", "needle")
	writePage(t, repo, "pages/c-tie.md", "page_tie_c", "Tie", "needle")

	searcher := FilesystemLexicalSearcher{}
	results, warnings, err := searcher.Search(context.Background(), repo, Query{Text: "Project Foo", Scope: ScopePages, Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	if len(results) < 3 {
		t.Fatalf("results: %+v", results)
	}
	if results[0].Path != "pages/a-title.md" {
		t.Fatalf("title match did not rank first: %+v", results)
	}
	var phrase, disconnected Result
	for _, result := range results {
		switch result.Path {
		case "pages/b-phrase.md":
			phrase = result
		case "pages/c-disconnected.md":
			disconnected = result
		}
	}
	if phrase.Score <= disconnected.Score {
		t.Fatalf("exact phrase score %d did not outrank disconnected score %d", phrase.Score, disconnected.Score)
	}
	if phrase.Snippet != "Before.\nProject Foo is deployable.\nAfter." || phrase.LineStart != 10 || phrase.LineEnd != 12 {
		t.Fatalf("phrase snippet: %+v", phrase)
	}

	ties, _, err := searcher.Search(context.Background(), repo, Query{Text: "needle", Scope: ScopePages, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var tiePaths []string
	for _, result := range ties {
		if strings.Contains(result.Path, "-tie") {
			tiePaths = append(tiePaths, result.Path)
		}
	}
	if len(tiePaths) != 2 || tiePaths[0] != "pages/c-tie.md" || tiePaths[1] != "pages/d-tie.md" {
		t.Fatalf("tie order = %v", tiePaths)
	}
}

func TestTruncateUTF8(t *testing.T) {
	value := strings.Repeat("a", 498) + "🦉tail"
	got := truncateUTF8(value, 500)
	if len(got) > 500 || !utf8.ValidString(got) {
		t.Fatalf("invalid truncation len=%d valid=%v", len(got), utf8.ValidString(got))
	}
	if got != strings.Repeat("a", 498) {
		t.Fatalf("truncation split or retained partial rune: %q", got[len(got)-10:])
	}
}

func searchRepository(t *testing.T) *repository.Repository {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"pages", "sources"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &repository.Repository{Root: root, Config: config.Defaults()}
}

func writePage(t *testing.T, repo *repository.Repository, path, id, title, body string) {
	t.Helper()
	data := []byte("---\nid: " + id + "\ntitle: " + title + "\nkind: topic\ncreated: \"2026-07-22\"\nupdated: \"2026-07-22\"\nstatus: active\nsensitivity: normal\n---\n" + body)
	if err := os.WriteFile(filepath.Join(repo.Root, filepath.FromSlash(path)), data, 0o644); err != nil {
		t.Fatal(err)
	}
}
