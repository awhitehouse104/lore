package core_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"lore/internal/core"
	"lore/internal/repository"
)

func TestPreflightBuildsIndexAndFastForwardsWithOneSynchronizedOperation(t *testing.T) {
	root, remote := preflightRepository(t)
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	service := core.NewService(repo)

	first, err := service.Preflight(context.Background(), core.PreflightOptions{Sync: true})
	if err != nil {
		t.Fatalf("first Preflight: %v", err)
	}
	if !first.Ready || first.Status != "ready" || first.Scope != "synchronized" ||
		!first.Remote.Checked || first.Remote.Ahead != 0 || first.Remote.Behind != 0 ||
		first.Index == nil || first.Index.IndexState != "fresh" || first.IndexAction != "built" ||
		first.Lint == nil || !first.Lint.Valid {
		t.Fatalf("first result = %+v", first)
	}

	writer := filepath.Join(t.TempDir(), "writer")
	runGit(t, t.TempDir(), "clone", "--branch", "main", remote, writer)
	readmePath := filepath.Join(writer, "README.md")
	readme, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(readmePath, append(readme, []byte("\nRemote maintenance.\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, writer, "add", "--", "README.md")
	runGit(t, writer, "commit", "-m", "maintenance: remote change")
	runGit(t, writer, "push", "origin", "main")

	second, err := service.Preflight(context.Background(), core.PreflightOptions{Sync: true})
	if err != nil {
		t.Fatalf("second Preflight: %v", err)
	}
	if !second.Ready || !second.Remote.Checked || !second.Remote.FastForwarded ||
		second.Remote.Ahead != 0 || second.Remote.Behind != 1 ||
		second.HeadBefore == second.HeadAfter || second.IndexAction != "updated" ||
		second.Index == nil || second.Index.IndexState != "fresh" || second.Lint == nil || !second.Lint.Valid ||
		second.Lint.Warnings != 0 {
		t.Fatalf("second result = %+v", second)
	}
	if data, err := os.ReadFile(filepath.Join(root, "README.md")); err != nil || !bytes.Contains(data, []byte("Remote maintenance.")) {
		t.Fatalf("fast-forwarded README missing remote change, err=%v", err)
	}
}

func TestPreflightFailsClosedBeforeFetchForDirtyWorktree(t *testing.T) {
	root, _ := preflightRepository(t)
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "local-note.md"), []byte("preserve me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := core.NewService(repo).Preflight(context.Background(), core.PreflightOptions{Sync: true})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if result.Ready || result.Remote.Checked || len(result.Blockers) != 1 || result.Blockers[0].Code != "worktree_dirty" {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Blockers[0].Changes) != 1 || result.Blockers[0].Changes[0].Path != "local-note.md" {
		t.Fatalf("dirty changes = %+v", result.Blockers[0].Changes)
	}
}

func TestPreflightBlocksAheadHistoryWithoutUpdatingIndex(t *testing.T) {
	root, _ := preflightRepository(t)
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	service := core.NewService(repo)
	if _, err := service.Preflight(context.Background(), core.PreflightOptions{Sync: true}); err != nil {
		t.Fatal(err)
	}
	readmePath := filepath.Join(root, "README.md")
	readme, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(readmePath, append(readme, []byte("\nLocal maintenance.\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "--", "README.md")
	runGit(t, root, "commit", "-m", "maintenance: local change")

	result, err := service.Preflight(context.Background(), core.PreflightOptions{Sync: true})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if result.Ready || !result.Remote.Checked || result.Remote.Ahead != 1 || result.Remote.Behind != 0 ||
		len(result.Blockers) != 1 || result.Blockers[0].Code != "git_ahead" || result.Index != nil {
		t.Fatalf("result = %+v", result)
	}
}

func TestPreflightDetectsPendingPreviewOwnedByAnyActorBeforeFetch(t *testing.T) {
	repo := transactionTestRepository(t)
	service := core.NewService(repo)
	service.Actor = "first_agent"
	page := validTransactionPage("page_preflight_pending", "Preflight pending", "2026-08-05", "Pending.\n")
	if _, err := service.Preview(context.Background(), transactionRequest(t, "create: pending preflight fixture", []map[string]any{{
		"op": "create_page", "path": "pages/preflight-pending.md", "content": string(page),
	}})); err != nil {
		t.Fatalf("Preview: %v", err)
	}
	service.Actor = "second_agent"
	result, err := service.Preflight(context.Background(), core.PreflightOptions{Sync: true})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if result.Ready || result.Remote.Checked || !result.Local.PendingPreview ||
		len(result.Blockers) != 1 || result.Blockers[0].Code != "pending_preview" {
		t.Fatalf("result = %+v", result)
	}
}

func preflightRepository(t *testing.T) (string, string) {
	t.Helper()
	root := initializeGitRepository(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, t.TempDir(), "init", "--bare", remote)
	runGit(t, root, "remote", "add", "origin", remote)
	runGit(t, root, "push", "-u", "origin", "main")
	return root, remote
}
