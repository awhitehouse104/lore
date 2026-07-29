package index

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"lore/internal/gitx"
	"lore/internal/repository"
)

func TestProbeFTS5AndCreateSchema(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := probeFTS5(ctx, db); err != nil {
		t.Fatalf("probeFTS5: %v", err)
	}
	transaction, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := createSchema(ctx, transaction); err != nil {
		t.Fatalf("createSchema: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	var tableCount int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name IN ('metadata', 'documents', 'documents_fts')").Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 3 {
		t.Fatalf("schema table count = %d, want 3", tableCount)
	}
}

func TestBuildStatusForceAndFailedReplacement(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	writeTestPage(t, repo.Root, "indexed body")
	manager := NewManager(repo, gitx.New(), "0.3.0-test")
	manager.Clock = fixedClock{value: time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)}

	result, err := manager.Build(ctx, BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if result.IndexState != StateUncertified || result.DocumentCount != 1 || result.PageCount != 1 {
		t.Fatalf("unexpected build result: %+v", result)
	}
	indexPath := filepath.Join(repo.Root, filepath.FromSlash(RelativeIndexPath))
	info, err := os.Stat(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("index mode = %o, want 600", info.Mode().Perm())
	}
	status, err := manager.Status(ctx, true)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.IndexState != StateUncertified || !status.ManifestMatches || status.Verification != "full" ||
		!status.RepositoryIdentityMatches || !status.SecureDelete || !status.FTS5SecureDelete {
		t.Fatalf("unexpected status: %+v", status)
	}
	if _, err := manager.Build(ctx, BuildOptions{}); err == nil {
		t.Fatal("second build without force unexpectedly succeeded")
	} else {
		var indexErr *Error
		if !errors.As(err, &indexErr) || indexErr.Code != "index_already_current" {
			t.Fatalf("second build error = %v", err)
		}
	}
	if _, err := manager.Build(ctx, BuildOptions{Force: true}); err != nil {
		t.Fatalf("forced build: %v", err)
	}

	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.Root, "pages", "bad.md"), []byte("not frontmatter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Build(ctx, BuildOptions{Force: true}); err == nil {
		t.Fatal("invalid canonical build unexpectedly succeeded")
	}
	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed build changed the previous index")
	}
}

func TestGitBuildFreshAndManagedEditStale(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	ctx := context.Background()
	repo := newTestRepository(t)
	writeTestPage(t, repo.Root, "committed body")
	runGit(t, repo.Root, "init", "-b", "main")
	runGit(t, repo.Root, "config", "user.name", "Lore Test")
	runGit(t, repo.Root, "config", "user.email", "lore@example.invalid")
	runGit(t, repo.Root, "add", "--", ".")
	runGit(t, repo.Root, "commit", "-m", "test: initialize")

	manager := NewManager(repo, gitx.New(), "0.3.0-test")
	manager.Clock = fixedClock{value: time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)}
	if _, err := manager.Build(ctx, BuildOptions{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	status, err := manager.Status(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if status.IndexState != StateFresh || !status.ManagedWorktreeClean {
		t.Fatalf("fresh status = %+v", status)
	}
	writeTestPage(t, repo.Root, "external edit")
	status, err = manager.Status(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if status.IndexState != StateStale || status.ManagedWorktreeClean {
		t.Fatalf("stale status = %+v", status)
	}
}

func TestGitFreshnessTracksOnlyManagedDirtyStateAndExactSnapshot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	ctx := context.Background()
	repo := newTestRepository(t)
	writeTestPage(t, repo.Root, "committed body")
	runGit(t, repo.Root, "init", "-b", "main")
	runGit(t, repo.Root, "config", "user.name", "Lore Test")
	runGit(t, repo.Root, "config", "user.email", "lore@example.invalid")
	runGit(t, repo.Root, "add", "--", ".")
	runGit(t, repo.Root, "commit", "-m", "test: initialize")
	manager := NewManager(repo, gitx.New(), "0.3.0-test")
	manager.Clock = fixedClock{value: time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)}
	if _, err := manager.Build(ctx, BuildOptions{}); err != nil {
		t.Fatal(err)
	}

	readme := filepath.Join(repo.Root, "README.md")
	if err := os.WriteFile(readme, []byte("unrelated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if status.IndexState != StateFresh {
		t.Fatalf("unrelated worktree state = %s, want fresh", status.IndexState)
	}
	runGit(t, repo.Root, "add", "--", "README.md")
	runGit(t, repo.Root, "commit", "-m", "docs: unrelated")
	status, err = manager.Status(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if status.IndexState != StateStale || !status.ManagedWorktreeClean {
		t.Fatalf("new HEAD status = %+v", status)
	}
	update, err := manager.Update(ctx)
	if err != nil {
		t.Fatalf("update new HEAD: %v", err)
	}
	if update.Unchanged != 1 || update.IndexState != StateFresh {
		t.Fatalf("new HEAD update = %+v", update)
	}

	runGit(t, repo.Root, "switch", "-c", "other")
	status, err = manager.Status(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if status.IndexState != StateStale {
		t.Fatalf("new branch state = %s, want stale", status.IndexState)
	}
	if _, err := manager.Update(ctx); err != nil {
		t.Fatalf("update new branch: %v", err)
	}
	runGit(t, repo.Root, "checkout", "--detach")
	status, err = manager.Status(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if status.IndexState != StateStale || status.CurrentBranch != detachedBranch {
		t.Fatalf("detached status = %+v", status)
	}
	if _, err := manager.Update(ctx); err != nil {
		t.Fatalf("update detached HEAD: %v", err)
	}
	status, err = manager.Status(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if status.IndexState != StateFresh || status.IndexedBranch != detachedBranch {
		t.Fatalf("updated detached status = %+v", status)
	}
}

type fixedClock struct {
	value time.Time
}

func (c fixedClock) Now() time.Time {
	return c.value
}

func newTestRepository(t *testing.T) *repository.Repository {
	t.Helper()
	root := t.TempDir()
	for _, directory := range []string{"pages", "sources", "assets", "system", ".lore"} {
		mode := os.FileMode(0o755)
		if directory == ".lore" {
			mode = 0o700
		}
		if err := os.Mkdir(filepath.Join(root, directory), mode); err != nil {
			t.Fatal(err)
		}
	}
	config := `version: 1
git:
  auto_commit_captures: false
  auto_push_captures: false
  auto_push_transactions: false
  remote: origin
  require_push: false
capture:
  max_bytes: 4194304
index:
  backend: auto
  auto_refresh_existing: true
  candidate_multiplier: 20
  minimum_candidates: 200
  maximum_candidates: 2000
`
	if err := os.WriteFile(filepath.Join(root, "lore.yaml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".lore/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func writeTestPage(t *testing.T, root, body string) {
	t.Helper()
	data := []byte(`---
id: page_index_test
title: Index Test
kind: note
created: "2026-07-29"
updated: "2026-07-29"
status: active
sensitivity: normal
aliases:
  - indexed alias
tags:
  - index
---
` + body + "\n")
	if err := os.WriteFile(filepath.Join(root, "pages", "index-test.md"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", root}, args...)
	command := exec.Command("git", commandArgs...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}
