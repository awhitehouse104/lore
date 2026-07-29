package core_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"lore/internal/core"
	loreindex "lore/internal/index"
)

func TestIndexBuildRequiresCleanManagedGitPaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	repo := newServiceRepository(t)
	runGit(t, repo.Root, "init", "-b", "main")
	runGit(t, repo.Root, "config", "user.name", "Lore Test")
	runGit(t, repo.Root, "config", "user.email", "lore@example.invalid")
	runGit(t, repo.Root, "add", "--", ".")
	runGit(t, repo.Root, "commit", "-m", "test: initialize")

	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}
	result, err := service.IndexBuild(context.Background(), core.IndexBuildOptions{})
	if err != nil {
		t.Fatalf("IndexBuild: %v", err)
	}
	if result.IndexState != loreindex.StateFresh {
		t.Fatalf("build state = %s", result.IndexState)
	}

	pagePath := filepath.Join(repo.Root, "pages", "untracked.md")
	if err := os.WriteFile(pagePath, []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = service.IndexBuild(context.Background(), core.IndexBuildOptions{Force: true})
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "managed_worktree_dirty" || apiErr.ExitCode != core.ExitConflict {
		t.Fatalf("dirty build error = %T %v", err, err)
	}
}
