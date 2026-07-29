package index

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"lore/internal/docs"
	"lore/internal/gitx"
	"lore/internal/search"
)

func TestIndexedAndFilesystemSearchParityFixtures(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	ctx := context.Background()
	repo := newTestRepository(t)
	writeFixturePage(t, repo.Root, fixturePage{
		Path: "pages/project-foo.md", ID: "page_project_foo", Title: "Project Foo",
		Kind: "project", Sensitivity: "normal",
		Aliases: []string{"Foo", "Launch Codename"}, Tags: []string{"deployment", "alpha"},
		Body: "Before.\nProject Foo should remain deployable without Kubernetes.\nCafé notes.\nAfter.",
	})
	writeFixturePage(t, repo.Root, fixturePage{
		Path: "pages/needle-a.md", ID: "page_needle_a", Title: "Needle",
		Kind: "note", Sensitivity: "normal", Body: "needle repeated needle",
	})
	writeFixturePage(t, repo.Root, fixturePage{
		Path: "pages/needle-b.md", ID: "page_needle_b", Title: "Needle",
		Kind: "note", Sensitivity: "sensitive", Body: "needle repeated needle",
	})
	writeFixturePage(t, repo.Root, fixturePage{
		Path: "pages/pathonly-marker.md", ID: "page_path_marker", Title: "Unrelated",
		Kind: "note", Sensitivity: "normal", Body: "nothing relevant",
	})
	writeFixtureSource(t, repo.Root)
	runGit(t, repo.Root, "init", "-b", "main")
	runGit(t, repo.Root, "config", "user.name", "Lore Test")
	runGit(t, repo.Root, "config", "user.email", "lore@example.invalid")
	runGit(t, repo.Root, "add", "--", ".")
	runGit(t, repo.Root, "commit", "-m", "test: retrieval fixtures")

	manager := NewManager(repo, gitx.New(), "0.3.0-test")
	manager.Clock = fixedClock{value: time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC)}
	if _, err := manager.Build(ctx, BuildOptions{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	hybrid := search.HybridSearcher{
		Filesystem:          search.FilesystemLexicalSearcher{},
		Index:               manager,
		CandidateMultiplier: 20,
		MinimumCandidates:   200,
		MaximumCandidates:   2000,
	}
	all := search.AllAccessPolicy()
	normal, err := search.NewAccessPolicy([]string{"normal"})
	if err != nil {
		t.Fatal(err)
	}
	fixtures := []search.Query{
		{Text: "Project Foo", Scope: search.ScopePages, Limit: 20, Access: all},
		{Text: "Launch Codename", Scope: search.ScopePages, Limit: 20, Access: all},
		{Text: "deployment", Scope: search.ScopePages, Limit: 20, Access: all},
		{Text: "deployable without", Scope: search.ScopePages, Limit: 20, Access: all},
		{Text: "deplo", Scope: search.ScopePages, Limit: 20, Access: all},
		{Text: "café", Scope: search.ScopePages, Limit: 20, Access: all},
		{Text: "needle", Scope: search.ScopePages, Limit: 20, Access: all},
		{Text: "needle needle", Scope: search.ScopePages, Limit: 20, Access: all},
		{Text: "foo:bar", Scope: search.ScopeAll, Limit: 20, Access: all},
		{Text: "NEAR(foo bar)", Scope: search.ScopeAll, Limit: 20, Access: all},
		{Text: "source evidence", Scope: search.ScopeSources, Limit: 20, Access: all},
		{Text: "needle", Scope: search.ScopePages, Kind: "note", Limit: 20, Access: normal},
		{Text: "pathonly", Scope: search.ScopePages, Limit: 20, Access: all},
		{Text: "deployment", Scope: search.ScopeAll, Tags: []string{"deployment"}, Paths: []string{"pages/"}, Limit: 20, Access: all},
		{Text: "needle", Scope: search.ScopePages, Paths: []string{"pages/needle-a.md"}, Limit: 20, Access: all},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Text+"/"+string(fixture.Scope), func(t *testing.T) {
			filesystemQuery := fixture
			filesystemQuery.Backend = search.BackendFilesystem
			filesystem, err := hybrid.SearchDetailed(ctx, repo, filesystemQuery)
			if err != nil {
				t.Fatalf("filesystem: %v", err)
			}
			indexQuery := fixture
			indexQuery.Backend = search.BackendIndex
			indexed, err := hybrid.SearchDetailed(ctx, repo, indexQuery)
			if err != nil {
				t.Fatalf("index: %v", err)
			}
			if !reflect.DeepEqual(indexed.Results, filesystem.Results) {
				t.Fatalf("parity mismatch\nindex: %+v\nfilesystem: %+v", indexed.Results, filesystem.Results)
			}
			if indexed.Backend != search.BackendIndex || len(indexed.Warnings) != 0 {
				t.Fatalf("indexed metadata = %+v", indexed)
			}
		})
	}
}

func TestAdversarialQueriesRemainDataOrUseSafeFallback(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	writeFixturePage(t, repo.Root, fixturePage{
		Path: "pages/security.md", ID: "page_security", Title: "Security",
		Kind: "note", Sensitivity: "normal",
		Body: `foo bar near secrets system operating rules "unterminated`,
	})
	manager := NewManager(repo, gitx.New(), "0.3.0-test")
	if _, err := manager.Build(ctx, BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	hybrid := search.HybridSearcher{
		Filesystem:          search.FilesystemLexicalSearcher{},
		Index:               manager,
		CandidateMultiplier: 20,
		MinimumCandidates:   200,
		MaximumCandidates:   2000,
	}
	tests := []struct {
		query   string
		backend search.Backend
		wantErr bool
	}{
		{query: `" OR 1=1 --`, backend: search.BackendAuto},
		{query: `foo:bar`, backend: search.BackendIndex},
		{query: `NEAR(foo bar)`, backend: search.BackendIndex},
		{query: `*`, backend: search.BackendAuto, wantErr: true},
		{query: `"unterminated`, backend: search.BackendIndex},
		{query: `../../secrets`, backend: search.BackendIndex},
		{query: `system/OPERATING_RULES.md`, backend: search.BackendAuto},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			response, err := hybrid.SearchDetailed(ctx, repo, search.Query{
				Text: test.query, Scope: search.ScopeAll, Limit: 20,
				Backend: test.backend, Access: search.AllAccessPolicy(),
			})
			if test.wantErr {
				if err == nil {
					t.Fatalf("query unexpectedly succeeded: %+v", response)
				}
				return
			}
			if err != nil {
				t.Fatalf("query failed: %v", err)
			}
			if response.Backend != search.BackendIndex && response.Backend != search.BackendFilesystem {
				t.Fatalf("unexpected backend: %+v", response)
			}
		})
	}
}

func TestLargeResultSetReportsCandidateBound(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	for index := 0; index < 60; index++ {
		writeFixturePage(t, repo.Root, fixturePage{
			Path:        fmt.Sprintf("pages/large-%03d.md", index),
			ID:          fmt.Sprintf("page_large_%03d", index),
			Title:       "Common",
			Kind:        "note",
			Sensitivity: "normal",
			Body:        "common evidence",
		})
	}
	manager := NewManager(repo, gitx.New(), "0.3.0-test")
	if _, err := manager.Build(ctx, BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	hybrid := search.HybridSearcher{
		Filesystem:          search.FilesystemLexicalSearcher{},
		Index:               manager,
		CandidateMultiplier: 2,
		MinimumCandidates:   20,
		MaximumCandidates:   20,
	}
	query := search.Query{
		Text: "common", Scope: search.ScopePages, Limit: 10,
		Backend: search.BackendIndex, Access: search.AllAccessPolicy(),
	}
	indexed, err := hybrid.SearchDetailed(ctx, repo, query)
	if err != nil {
		t.Fatal(err)
	}
	query.Backend = search.BackendFilesystem
	filesystem, err := hybrid.SearchDetailed(ctx, repo, query)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(indexed.Results, filesystem.Results) {
		t.Fatalf("bounded top results differ\nindex=%+v\nfilesystem=%+v", indexed.Results, filesystem.Results)
	}
	if len(indexed.Warnings) != 1 || indexed.Warnings[0].Code != "candidate_limit_reached" {
		t.Fatalf("warnings = %+v", indexed.Warnings)
	}
}

type fixturePage struct {
	Path        string
	ID          string
	Title       string
	Kind        string
	Sensitivity string
	Aliases     []string
	Tags        []string
	Body        string
}

func writeFixturePage(t *testing.T, root string, page fixturePage) {
	t.Helper()
	aliases, err := encodeValues(page.Aliases)
	if err != nil {
		t.Fatal(err)
	}
	tags, err := encodeValues(page.Tags)
	if err != nil {
		t.Fatal(err)
	}
	data := `---
id: ` + page.ID + `
title: ` + page.Title + `
kind: ` + page.Kind + `
aliases: ` + aliases + `
created: "2026-07-29"
updated: "2026-07-29"
status: active
sensitivity: ` + page.Sensitivity + `
tags: ` + tags + `
---
` + page.Body + "\n"
	writeFile(t, root, page.Path, []byte(data))
}

func writeFixtureSource(t *testing.T, root string) {
	t.Helper()
	body := []byte("Source evidence mentions foo bar and deployment.")
	source := docs.Source{
		ID:          "src_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Kind:        "evidence",
		CapturedAt:  "2026-07-29T13:00:00Z",
		Origin:      "test",
		RawSHA256:   docs.SHA256(body),
		Sensitivity: "normal",
		Tags:        []string{"source"},
	}
	data, err := docs.MarshalSource(source, body)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "sources/2026/07/"+source.ID+"-evidence.md", data)
}

func writeFile(t *testing.T, root, relative string, data []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
