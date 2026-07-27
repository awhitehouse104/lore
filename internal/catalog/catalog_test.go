package catalog

import (
	"os"
	"path/filepath"
	"testing"

	"lore/internal/config"
	"lore/internal/docs"
	"lore/internal/repository"
)

func TestResolvePriorityAndAmbiguity(t *testing.T) {
	repo := &repository.Repository{Root: t.TempDir(), Config: config.Defaults()}
	for _, dir := range []string{"pages", "sources"} {
		if err := os.Mkdir(filepath.Join(repo.Root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	documents := []*docs.Document{
		pageDocument("pages/one.md", "page_one", "One", []string{"shared"}),
		pageDocument("pages/two.md", "page_two", "Two", []string{"shared"}),
		pageDocument("pages/page_one.md", "page_three", "Three", nil),
	}
	catalog := Catalog{Documents: documents}

	got, err := catalog.Resolve(repo, "page_one")
	if err != nil {
		t.Fatalf("Resolve ID: %v", err)
	}
	if got.Path != "pages/one.md" {
		t.Fatalf("ID priority resolved %q", got.Path)
	}
	if _, err := catalog.Resolve(repo, "shared"); err == nil {
		t.Fatal("ambiguous alias unexpectedly resolved")
	} else if ambiguous, ok := err.(*AmbiguousError); !ok || len(ambiguous.Candidates) != 2 {
		t.Fatalf("ambiguity error = %T %+v", err, err)
	}
	if _, err := catalog.Resolve(repo, "../outside"); err == nil {
		t.Fatal("traversal unexpectedly resolved")
	}
}

func pageDocument(path, id, title string, aliases []string) *docs.Document {
	return &docs.Document{
		Path: path,
		Type: docs.TypePage,
		Page: &docs.Page{ID: id, Title: title, Aliases: aliases},
	}
}
