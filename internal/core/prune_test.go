package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lore/internal/core"
	"lore/internal/docs"
	"lore/internal/lock"
	"lore/internal/search"
	"lore/internal/transaction"
)

func TestTransactionPruneDryRunCompactionAndLocalReceiptBehavior(t *testing.T) {
	repo := transactionTestRepository(t)
	service := core.NewService(repo)
	committedAt := time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)
	preview := createCommittedPruneTransaction(
		t,
		service,
		fixedTransactionID,
		"page_prune",
		"pages/prune.md",
		committedAt,
	)
	service.Clock = fixedClock{value: committedAt.Add(24 * time.Hour)}
	options := core.TransactionPruneOptions{
		OlderThan: 24 * time.Hour,
		Limit:     core.DefaultTransactionPruneLimit,
		DryRun:    true,
	}
	dryRun, err := service.TransactionPrune(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !dryRun.DryRun || dryRun.Eligible != 1 || dryRun.Selected != 1 ||
		dryRun.Pruned != 0 || len(dryRun.Transactions) != 1 ||
		dryRun.Transactions[0].TransactionID != fixedTransactionID ||
		dryRun.Transactions[0].ReclaimableFiles != 3 ||
		dryRun.FilesReclaimable != 3 || dryRun.BytesReclaimable == 0 {
		t.Fatalf("dry-run result = %+v", dryRun)
	}
	transactionDir := filepath.Join(repo.Root, ".lore", "transactions", fixedTransactionID)
	if _, err := os.Stat(filepath.Join(transactionDir, "diff.patch")); err != nil {
		t.Fatalf("dry-run removed payload: %v", err)
	}

	options.DryRun = false
	pruned, err := service.TransactionPrune(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if pruned.Pruned != 1 || pruned.FilesRemoved != 3 || pruned.BytesRemoved == 0 ||
		len(pruned.Transactions) != 1 || pruned.Transactions[0].ArtifactState != "pruned" {
		t.Fatalf("prune result = %+v", pruned)
	}
	shown, err := service.TransactionShow(fixedTransactionID, true)
	if err != nil {
		t.Fatal(err)
	}
	if shown.State.Status != transaction.StatusCommitted || shown.Retention == nil ||
		shown.Retention.Phase != transaction.RetentionPruned || shown.Diff != "" ||
		!shown.Lint.Valid {
		t.Fatalf("pruned transaction show = %+v", shown)
	}
	listed, err := service.TransactionList("", core.DefaultTransactionLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Transactions) != 1 || listed.Transactions[0].ArtifactState != "pruned" {
		t.Fatalf("pruned transaction list = %+v", listed)
	}
	owned, err := service.TransactionListOwned(
		context.Background(),
		"",
		core.DefaultTransactionLimit,
		search.AllAccessPolicy(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(owned.Transactions) != 0 {
		t.Fatalf("pruned receipt remained visible through authorized MCP path: %+v", owned)
	}
	if _, err := service.TransactionShowOwned(
		context.Background(),
		fixedTransactionID,
		false,
		search.AllAccessPolicy(),
	); err == nil {
		t.Fatal("pruned receipt remained directly readable through authorized MCP path")
	}
	repeated, err := service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID,
		PreviewDigest: preview.PreviewDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !repeated.AlreadyCommitted || repeated.Commit == "" {
		t.Fatalf("repeated commit = %+v", repeated)
	}
	again, err := service.TransactionPrune(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if again.Selected != 0 || again.Pruned != 0 || again.AlreadyPruned != 1 {
		t.Fatalf("idempotent core prune = %+v", again)
	}
}

func TestTransactionPruneVerifiesCommittedDeletionWithoutContentArtifact(t *testing.T) {
	repo := transactionTestRepository(t)
	page := validTransactionPage("page_pruned_delete", "Pruned delete", "2026-07-28", "Delete.\n")
	path := filepath.Join(repo.Root, "pages", "pruned-delete.md")
	if err := os.WriteFile(path, page, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.Root, "add", "--", "pages/pruned-delete.md")
	runGit(t, repo.Root, "commit", "-m", "maintenance: deletion fixture")
	committedAt := time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)
	service := core.NewService(repo)
	service.Clock = fixedClock{value: committedAt}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	preview, err := service.Preview(context.Background(), transactionRequest(t, "archive: prune deletion", []map[string]any{{
		"op": "delete_page", "path": "pages/pruned-delete.md", "expected_revision": docs.Revision(page),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest,
	}); err != nil {
		t.Fatal(err)
	}
	service.Clock = fixedClock{value: committedAt.Add(48 * time.Hour)}
	dryRun, err := service.TransactionPrune(context.Background(), core.TransactionPruneOptions{
		OlderThan: 24 * time.Hour,
		Limit:     1,
		DryRun:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.Selected != 1 || dryRun.FilesReclaimable != 2 {
		t.Fatalf("deletion prune dry-run = %+v", dryRun)
	}
	pruned, err := service.TransactionPrune(context.Background(), core.TransactionPruneOptions{
		OlderThan: 24 * time.Hour,
		Limit:     1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pruned.Pruned != 1 || pruned.FilesRemoved != 2 {
		t.Fatalf("deletion prune = %+v", pruned)
	}
}

func TestTransactionPruneUsesCommittedAtAndOldestFirstLimit(t *testing.T) {
	repo := transactionTestRepository(t)
	service := core.NewService(repo)
	firstAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	secondAt := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	firstID := fixedTransactionID
	secondID := "tx_01ARZ3NDEKTSV4RRFFQ69G5FAW"
	createCommittedPruneTransaction(t, service, firstID, "page_prune_first", "pages/prune-first.md", firstAt)
	createCommittedPruneTransaction(t, service, secondID, "page_prune_second", "pages/prune-second.md", secondAt)

	store, err := transaction.NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.LoadState(firstID)
	if err != nil {
		t.Fatal(err)
	}
	state.UpdatedAt = "2027-07-28T12:00:00Z"
	if err := store.UpdateState(firstID, state); err != nil {
		t.Fatal(err)
	}

	service.Clock = fixedClock{value: secondAt.Add(24 * time.Hour)}
	result, err := service.TransactionPrune(context.Background(), core.TransactionPruneOptions{
		OlderThan: 24 * time.Hour,
		Limit:     1,
		DryRun:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Eligible != 2 || result.Selected != 1 || result.Remaining != 1 ||
		len(result.Transactions) != 1 || result.Transactions[0].TransactionID != firstID {
		t.Fatalf("limited selection = %+v", result)
	}
}

func TestTransactionPruneRequiresReachableExactCommit(t *testing.T) {
	t.Run("unreachable", func(t *testing.T) {
		repo := transactionTestRepository(t)
		service := core.NewService(repo)
		committedAt := time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)
		createCommittedPruneTransaction(
			t,
			service,
			fixedTransactionID,
			"page_unreachable",
			"pages/unreachable.md",
			committedAt,
		)
		runGit(t, repo.Root, "update-ref", "-d", "refs/heads/main")
		service.Clock = fixedClock{value: committedAt.Add(48 * time.Hour)}
		_, err := service.TransactionPrune(context.Background(), core.TransactionPruneOptions{
			OlderThan: 24 * time.Hour,
			Limit:     100,
		})
		var apiErr *core.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != "transaction_commit_unreachable" {
			t.Fatalf("unreachable error = %#v", err)
		}
		assertTransactionPayloadRetained(t, repo.Root, fixedTransactionID)
	})

	t.Run("mismatched paths", func(t *testing.T) {
		repo := transactionTestRepository(t)
		service := core.NewService(repo)
		committedAt := time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)
		createCommittedPruneTransaction(
			t,
			service,
			fixedTransactionID,
			"page_mismatch",
			"pages/mismatch.md",
			committedAt,
		)
		rootCommit := strings.TrimSpace(runGit(t, repo.Root, "rev-list", "--max-parents=0", "HEAD"))
		store, err := transaction.NewStore(repo)
		if err != nil {
			t.Fatal(err)
		}
		state, err := store.LoadState(fixedTransactionID)
		if err != nil {
			t.Fatal(err)
		}
		state.Commit = rootCommit
		if err := store.UpdateState(fixedTransactionID, state); err != nil {
			t.Fatal(err)
		}
		service.Clock = fixedClock{value: committedAt.Add(48 * time.Hour)}
		_, err = service.TransactionPrune(context.Background(), core.TransactionPruneOptions{
			OlderThan: 24 * time.Hour,
			Limit:     100,
		})
		var apiErr *core.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != "transaction_commit_mismatch" {
			t.Fatalf("mismatch error = %#v", err)
		}
		assertTransactionPayloadRetained(t, repo.Root, fixedTransactionID)
	})
}

func TestTransactionPruneRespectsRecoveryAndWriteLock(t *testing.T) {
	repo := transactionTestRepository(t)
	service := core.NewService(repo)
	service.WriteLockWait = 0
	committedAt := time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)
	createCommittedPruneTransaction(
		t,
		service,
		fixedTransactionID,
		"page_blocked_prune",
		"pages/blocked-prune.md",
		committedAt,
	)
	service.Clock = fixedClock{value: committedAt.Add(48 * time.Hour)}
	options := core.TransactionPruneOptions{OlderThan: 24 * time.Hour, Limit: 100}

	handle, err := lock.Acquire(context.Background(), repo.Root, "test holder", service.Clock.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.TransactionPrune(context.Background(), options)
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "repository_locked" {
		t.Fatalf("lock error = %#v", err)
	}
	if err := handle.Release(); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(repo.Root, ".lore", "recovery", "active"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = service.TransactionPrune(context.Background(), options)
	if !errors.As(err, &apiErr) || apiErr.Code != "recovery_required" {
		t.Fatalf("recovery error = %#v", err)
	}
	assertTransactionPayloadRetained(t, repo.Root, fixedTransactionID)
}

func createCommittedPruneTransaction(
	t *testing.T,
	service *core.Service,
	transactionID, pageID, path string,
	now time.Time,
) core.PreviewResult {
	t.Helper()
	service.Clock = fixedClock{value: now}
	service.TxIDs = fixedTransactionIDs{value: transactionID}
	page := validTransactionPage(pageID, pageID, now.Format("2006-01-02"), "Prunable body.\n")
	preview, err := service.Preview(context.Background(), transactionRequest(t, "create: prune fixture", []map[string]any{{
		"op": "create_page", "path": path, "content": string(page),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID,
		PreviewDigest: preview.PreviewDigest,
	}); err != nil {
		t.Fatal(err)
	}
	return preview
}

func assertTransactionPayloadRetained(t *testing.T, root, transactionID string) {
	t.Helper()
	dir := filepath.Join(root, ".lore", "transactions", transactionID)
	for _, relative := range []string{"diff.patch", "lint.json", "content"} {
		if _, err := os.Lstat(filepath.Join(dir, relative)); err != nil {
			t.Fatalf("transaction payload %s was removed: %v", relative, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "retention.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retention marker was written before Git proof: %v", err)
	}
}
