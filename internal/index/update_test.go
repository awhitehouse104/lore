package index

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lore/internal/docs"
	"lore/internal/gitx"
)

func TestUpdateAddChangeDeleteAndNoOp(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	writePage(t, repo.Root, "pages/alpha.md", "page_alpha", "Alpha", "first body")
	manager := NewManager(repo, gitx.New(), "0.3.0-test")
	manager.Clock = fixedClock{value: time.Date(2026, 7, 29, 11, 0, 0, 0, time.UTC)}
	if _, err := manager.Build(ctx, BuildOptions{}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	writePage(t, repo.Root, "pages/alpha.md", "page_alpha", "Alpha", "changed body")
	writePage(t, repo.Root, "pages/beta.md", "page_beta", "Beta", "second body")
	result, err := manager.Update(ctx)
	if err != nil {
		t.Fatalf("Update add/change: %v", err)
	}
	if result.Added != 1 || result.Updated != 1 || result.Deleted != 0 || result.Unchanged != 0 {
		t.Fatalf("add/change result = %+v", result)
	}
	assertIndexedBody(t, repo.Root, "pages/alpha.md", "changed body\n")
	assertIndexedBody(t, repo.Root, "pages/beta.md", "second body\n")

	if err := os.Remove(filepath.Join(repo.Root, "pages", "alpha.md")); err != nil {
		t.Fatal(err)
	}
	result, err = manager.Update(ctx)
	if err != nil {
		t.Fatalf("Update delete: %v", err)
	}
	if result.Added != 0 || result.Updated != 0 || result.Deleted != 1 || result.Unchanged != 1 {
		t.Fatalf("delete result = %+v", result)
	}
	result, err = manager.Update(ctx)
	if err != nil {
		t.Fatalf("Update no-op: %v", err)
	}
	if result.Added != 0 || result.Updated != 0 || result.Deleted != 0 || result.Unchanged != 1 {
		t.Fatalf("no-op result = %+v", result)
	}
	status, err := manager.Status(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if status.IndexState != StateUncertified || !status.ManifestMatches || status.DocumentCount != 1 {
		t.Fatalf("status = %+v", status)
	}
}

func TestClearIsIdempotentAndRejectsDerivedSymlink(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	manager := NewManager(repo, gitx.New(), "0.3.0-test")
	if _, err := manager.Build(ctx, BuildOptions{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	buildPath := filepath.Join(repo.Root, ".lore", "index.build.abandoned.sqlite")
	if err := os.WriteFile(buildPath, []byte("abandoned"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Clear()
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if !result.Existed || len(result.Removed) < 2 {
		t.Fatalf("clear result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(repo.Root, ".lore", "repository-id")); err != nil {
		t.Fatalf("repository identity removed: %v", err)
	}
	second, err := manager.Clear()
	if err != nil {
		t.Fatalf("second Clear: %v", err)
	}
	if second.Existed || len(second.Removed) != 0 {
		t.Fatalf("second clear = %+v", second)
	}

	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(repo.Root, ".lore", "index.build.evil.sqlite")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Clear(); err == nil {
		t.Fatal("Clear unexpectedly accepted a derived symlink")
	} else {
		var indexErr *Error
		if !errors.As(err, &indexErr) || indexErr.Code != "unsafe_index_path" {
			t.Fatalf("clear symlink error = %T %v", err, err)
		}
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "preserve" {
		t.Fatalf("outside file changed: %q, %v", data, err)
	}
}

func TestSourceBodyIsIndexedWithoutChangingCanonicalBytes(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	body := []byte("exact source\r\nwithout final newline")
	source := docs.Source{
		ID:          "src_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Kind:        "note",
		CapturedAt:  "2026-07-29T12:00:00Z",
		Origin:      "test",
		RawSHA256:   docs.SHA256(body),
		Sensitivity: "sensitive",
	}
	data, err := docs.MarshalSource(source, body)
	if err != nil {
		t.Fatal(err)
	}
	relative := "sources/2026/07/" + source.ID + "-note.md"
	path := filepath.Join(repo.Root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(repo, gitx.New(), "0.3.0-test")
	if _, err := manager.Build(ctx, BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	assertIndexedBody(t, repo.Root, relative, string(body))
	if _, err := manager.Update(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(data) {
		t.Fatal("index maintenance changed canonical source bytes")
	}
}

func TestStatusDetectsCorruptIncompatibleAndWrongIdentity(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
		full   bool
		state  State
	}{
		{
			name: "corrupt",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, ".lore", "index.sqlite"), []byte("not sqlite"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			state: StateCorrupt,
		},
		{
			name: "corrupt exact lexical terms",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				db, err := sql.Open("sqlite", filepath.Join(root, ".lore", "index.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("DELETE FROM document_terms WHERE term='indexed'"); err != nil {
					_ = db.Close()
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			},
			full:  true,
			state: StateCorrupt,
		},
		{
			name: "incompatible previous schema",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				updateMetadataForTest(t, root, "index_schema_version", "2")
			},
			state: StateIncompatible,
		},
		{
			name: "incompatible schema",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				updateMetadataForTest(t, root, "index_schema_version", "999")
			},
			state: StateIncompatible,
		},
		{
			name: "corrupt exact lexical term length",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				db, err := sql.Open("sqlite", filepath.Join(root, ".lore", "index.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("UPDATE document_terms SET rune_length=rune_length+1 WHERE term='indexed'"); err != nil {
					_ = db.Close()
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			},
			full:  true,
			state: StateCorrupt,
		},
		{
			name: "wrong identity",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				updateMetadataForTest(t, root, "repository_identity", "uuid:00000000-0000-4000-8000-000000000000")
			},
			state: StateIncompatible,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newTestRepository(t)
			writeTestPage(t, repo.Root, "indexed body")
			manager := NewManager(repo, gitx.New(), "0.3.0-test")
			if _, err := manager.Build(ctx, BuildOptions{}); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, repo.Root)
			status, err := manager.Status(ctx, test.full)
			if err != nil {
				t.Fatal(err)
			}
			if status.IndexState != test.state {
				t.Fatalf("state = %s, want %s; status=%+v", status.IndexState, test.state, status)
			}
		})
	}
}

func writePage(t *testing.T, root, relative, id, title, body string) {
	t.Helper()
	data := []byte(`---
id: ` + id + `
title: ` + title + `
kind: note
created: "2026-07-29"
updated: "2026-07-29"
status: active
sensitivity: normal
---
` + body + "\n")
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertIndexedBody(t *testing.T, root, path, want string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, ".lore", "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var body string
	if err := db.QueryRow("SELECT body FROM documents WHERE path=?", path).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != want {
		t.Fatalf("indexed body for %s = %q, want %q", path, body, want)
	}
}

func updateMetadataForTest(t *testing.T, root, key, value string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, ".lore", "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE metadata SET value=? WHERE key=?", value, key); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}
