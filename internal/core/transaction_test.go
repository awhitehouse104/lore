package core_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lore/internal/core"
	"lore/internal/docs"
	"lore/internal/gitx"
	"lore/internal/initrepo"
	"lore/internal/repository"
)

const fixedTransactionID = "tx_01ARZ3NDEKTSV4RRFFQ69G5FAV"

type fixedTransactionIDs struct {
	value string
}

func (g fixedTransactionIDs) New(time.Time) (string, error) {
	return g.value, nil
}

func TestPreviewCreateInspectAndDiscard(t *testing.T) {
	repo := transactionTestRepository(t)
	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	page := validTransactionPage("page_new", "New page", "2026-07-28", "Body.\n")
	request := transactionRequest(t, "create: new page", []map[string]any{{
		"op": "create_page", "path": "pages/new.md", "content": string(page),
	}})

	result, err := service.Preview(context.Background(), request)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if result.Status != "previewed" || result.TransactionID != fixedTransactionID || result.PreviewDigest == "" {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(result.Diff, "--- /dev/null") ||
		!strings.Contains(result.Diff, "+++ b/pages/new.md") {
		t.Fatalf("diff = %s", result.Diff)
	}
	if _, err := os.Stat(filepath.Join(repo.Root, "pages", "new.md")); !os.IsNotExist(err) {
		t.Fatalf("preview changed working tree: %v", err)
	}

	shown, err := service.TransactionShow(fixedTransactionID, true)
	if err != nil {
		t.Fatalf("TransactionShow: %v", err)
	}
	if shown.PreviewDigest != result.PreviewDigest || shown.Diff != result.Diff || !shown.Lint.Valid {
		t.Fatalf("shown = %+v", shown)
	}
	listed, err := service.TransactionList("", core.DefaultTransactionLimit)
	if err != nil {
		t.Fatalf("TransactionList: %v", err)
	}
	if len(listed.Transactions) != 1 || listed.Transactions[0].TransactionID != fixedTransactionID {
		t.Fatalf("listed = %+v", listed)
	}
	discarded, err := service.TransactionDiscard(fixedTransactionID)
	if err != nil {
		t.Fatalf("TransactionDiscard: %v", err)
	}
	if discarded.Status != "discarded" {
		t.Fatalf("discarded = %+v", discarded)
	}
	if _, err := service.TransactionDiscard(fixedTransactionID); err != nil {
		t.Fatalf("idempotent TransactionDiscard: %v", err)
	}
}

func TestPreviewRejectsProspectiveDuplicateWithoutPersisting(t *testing.T) {
	repo := transactionTestRepository(t)
	existing := validTransactionPage("page_duplicate", "Existing", "2026-07-28", "Existing.\n")
	if err := os.WriteFile(filepath.Join(repo.Root, "pages", "existing.md"), existing, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.Root, "add", "--", "pages/existing.md")
	runGit(t, repo.Root, "commit", "-m", "maintenance: fixture")

	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	duplicate := validTransactionPage("page_duplicate", "Duplicate", "2026-07-28", "Duplicate.\n")
	request := transactionRequest(t, "create: duplicate", []map[string]any{{
		"op": "create_page", "path": "pages/duplicate.md", "content": string(duplicate),
	}})
	result, err := service.Preview(context.Background(), request)
	if err == nil {
		t.Fatal("invalid prospective repository was accepted")
	}
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.ExitCode != core.ExitValidation || apiErr.Code != "prospective_lint_invalid" {
		t.Fatalf("error = %#v", err)
	}
	if result.Status != "invalid" || result.Lint.Valid {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(repo.Root, ".lore", "transactions", fixedTransactionID)); !os.IsNotExist(err) {
		t.Fatalf("invalid preview was persisted: %v", err)
	}
}

func TestPreviewUpdateAndSourceIntegrationPreserveSourceBody(t *testing.T) {
	repo := transactionTestRepository(t)
	pagePath := filepath.Join(repo.Root, "pages", "existing.md")
	page := validTransactionPage("page_existing", "Existing", "2026-07-27", "Old.\n")
	if err := os.WriteFile(pagePath, page, 0o644); err != nil {
		t.Fatal(err)
	}
	body := []byte("raw\r\nsource\nbytes")
	source := docs.Source{
		ID:          fixedID,
		Kind:        "user_statement",
		CapturedAt:  "2026-07-28T18:00:00Z",
		Origin:      "test",
		RawSHA256:   docs.SHA256(body),
		Sensitivity: "normal",
	}
	sourceData, err := docs.MarshalSource(source, body)
	if err != nil {
		t.Fatal(err)
	}
	sourceRelative := "sources/2026/07/" + fixedID + "-user_statement.md"
	sourceAbsolute := filepath.Join(repo.Root, filepath.FromSlash(sourceRelative))
	if err := os.MkdirAll(filepath.Dir(sourceAbsolute), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourceAbsolute, sourceData, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.Root, "add", "--", "pages/existing.md", sourceRelative)
	runGit(t, repo.Root, "commit", "-m", "maintenance: fixtures")

	updatedPage := validTransactionPage("page_existing", "Existing", "2026-07-28", "New.\n")
	request := transactionRequest(t, "integrate: source", []map[string]any{
		{
			"op": "update_page", "path": "pages/existing.md",
			"expected_revision": docs.Revision(page), "content": string(updatedPage),
		},
		{
			"op": "mark_source_integrated", "path": sourceRelative,
			"expected_revision": docs.Revision(sourceData), "page_ids": []string{"page_existing"},
		},
	})
	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	result, err := service.Preview(context.Background(), request)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(result.ChangedPaths) != 2 || !strings.Contains(result.Diff, "integrated_into:") {
		t.Fatalf("result = %+v", result)
	}
	shown, err := service.TransactionShow(fixedTransactionID, false)
	if err != nil {
		t.Fatal(err)
	}
	var sourceContent []byte
	for index, operation := range shown.Proposal.Operations {
		if operation.Path == sourceRelative {
			storePath := filepath.Join(repo.Root, ".lore", "transactions", fixedTransactionID, filepath.FromSlash(operation.ContentFile))
			sourceContent, err = os.ReadFile(storePath)
			if err != nil {
				t.Fatal(err)
			}
			_ = index
		}
	}
	document, err := docs.Parse(sourceRelative, sourceContent)
	if err != nil {
		t.Fatal(err)
	}
	if string(document.Body) != string(body) || docs.SHA256(document.Body) != source.RawSHA256 {
		t.Fatal("source body changed in effective operation")
	}
}

func TestPreviewRejectsRevisionMismatchAndDirtyTarget(t *testing.T) {
	repo := transactionTestRepository(t)
	page := validTransactionPage("page_existing", "Existing", "2026-07-28", "Original.\n")
	path := filepath.Join(repo.Root, "pages", "existing.md")
	if err := os.WriteFile(path, page, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.Root, "add", "--", "pages/existing.md")
	runGit(t, repo.Root, "commit", "-m", "maintenance: fixture")
	proposed := validTransactionPage("page_existing", "Existing", "2026-07-28", "Changed.\n")
	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}

	request := transactionRequest(t, "update: stale", []map[string]any{{
		"op": "update_page", "path": "pages/existing.md",
		"expected_revision": docs.SHA256([]byte("stale")), "content": string(proposed),
	}})
	_, err := service.Preview(context.Background(), request)
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "revision_conflict" || apiErr.ExitCode != core.ExitConflict {
		t.Fatalf("revision mismatch error = %#v", err)
	}

	if err := os.WriteFile(path, append(page, []byte("dirty")...), 0o644); err != nil {
		t.Fatal(err)
	}
	request = transactionRequest(t, "update: dirty", []map[string]any{{
		"op": "update_page", "path": "pages/existing.md",
		"expected_revision": docs.Revision(page), "content": string(proposed),
	}})
	_, err = service.Preview(context.Background(), request)
	if !errors.As(err, &apiErr) || apiErr.Code != "target_path_dirty" || apiErr.ExitCode != core.ExitConflict {
		t.Fatalf("dirty target error = %#v", err)
	}
}

func TestCommitCreateIsExactAndIdempotent(t *testing.T) {
	repo := transactionTestRepository(t)
	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	page := validTransactionPage("page_committed", "Committed", "2026-07-28", "Committed body.\n")
	preview, err := service.Preview(context.Background(), transactionRequest(t, "create: committed page", []map[string]any{{
		"op": "create_page", "path": "pages/committed.md", "content": string(page),
	}}))
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	before := runGit(t, repo.Root, "rev-parse", "HEAD")
	result, err := service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest,
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if result.Status != "committed" || result.Commit == "" || result.AlreadyCommitted {
		t.Fatalf("result = %+v", result)
	}
	if result.Commit == strings.TrimSpace(before) {
		t.Fatal("commit did not advance HEAD")
	}
	data, err := os.ReadFile(filepath.Join(repo.Root, "pages", "committed.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(page) {
		t.Fatal("committed page bytes differ")
	}
	paths, err := gitx.New().ChangedPathsInCommit(context.Background(), repo.Root, result.Commit)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "pages/committed.md" {
		t.Fatalf("commit paths = %v", paths)
	}
	if _, err := os.Stat(filepath.Join(repo.Root, ".lore", "recovery", "active")); !os.IsNotExist(err) {
		t.Fatalf("recovery journal remains: %v", err)
	}
	repeated, err := service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest,
	})
	if err != nil {
		t.Fatalf("idempotent Commit: %v", err)
	}
	if !repeated.AlreadyCommitted || repeated.Commit != result.Commit {
		t.Fatalf("repeated result = %+v", repeated)
	}
}

func TestCommitMultiplePathsPreservesUnrelatedGitState(t *testing.T) {
	repo := transactionTestRepository(t)
	first := validTransactionPage("page_first", "First", "2026-07-27", "First old.\n")
	second := validTransactionPage("page_second", "Second", "2026-07-27", "Second old.\n")
	if err := os.WriteFile(filepath.Join(repo.Root, "pages", "first.md"), first, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.Root, "pages", "second.md"), second, 0o644); err != nil {
		t.Fatal(err)
	}
	body := []byte("exact source\r\nbody")
	source := docs.Source{
		ID:          fixedID,
		Kind:        "user_statement",
		CapturedAt:  "2026-07-28T18:00:00Z",
		Origin:      "test",
		RawSHA256:   docs.SHA256(body),
		Sensitivity: "normal",
	}
	sourceData, err := docs.MarshalSource(source, body)
	if err != nil {
		t.Fatal(err)
	}
	sourceRelative := "sources/2026/07/" + fixedID + "-user_statement.md"
	sourceAbsolute := filepath.Join(repo.Root, filepath.FromSlash(sourceRelative))
	if err := os.MkdirAll(filepath.Dir(sourceAbsolute), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourceAbsolute, sourceData, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.Root, "add", "--", "pages/first.md", "pages/second.md", sourceRelative)
	runGit(t, repo.Root, "commit", "-m", "maintenance: transaction fixtures")

	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	firstNew := validTransactionPage("page_first", "First", "2026-07-28", "First new.\n")
	secondNew := validTransactionPage("page_second", "Second", "2026-07-28", "Second new.\n")
	preview, err := service.Preview(context.Background(), transactionRequest(t, "integrate: update two pages", []map[string]any{
		{"op": "update_page", "path": "pages/first.md", "expected_revision": docs.Revision(first), "content": string(firstNew)},
		{"op": "update_page", "path": "pages/second.md", "expected_revision": docs.Revision(second), "content": string(secondNew)},
		{"op": "mark_source_integrated", "path": sourceRelative, "expected_revision": docs.Revision(sourceData), "page_ids": []string{"page_first", "page_second"}},
	}))
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	operatingRules := filepath.Join(repo.Root, "system", "OPERATING_RULES.md")
	if err := os.WriteFile(operatingRules, append(mustRead(t, operatingRules), []byte("\nstaged unrelated\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.Root, "add", "--", "system/OPERATING_RULES.md")
	readme := filepath.Join(repo.Root, "README.md")
	if err := os.WriteFile(readme, append(mustRead(t, readme), []byte("\nunstaged unrelated\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	unrelatedBefore := runGit(t, repo.Root, "status", "--porcelain=v1", "-z", "--", "system/OPERATING_RULES.md", "README.md")

	result, err := service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest,
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	paths, err := gitx.New().ChangedPathsInCommit(context.Background(), repo.Root, result.Commit)
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"pages/first.md", "pages/second.md", sourceRelative}
	if strings.Join(paths, "\n") != strings.Join(expected, "\n") {
		t.Fatalf("commit paths = %v, want %v", paths, expected)
	}
	unrelatedAfter := runGit(t, repo.Root, "status", "--porcelain=v1", "-z", "--", "system/OPERATING_RULES.md", "README.md")
	if unrelatedAfter != unrelatedBefore {
		t.Fatalf("unrelated Git state changed:\nbefore %q\nafter  %q", unrelatedBefore, unrelatedAfter)
	}
	document, err := docs.Parse(sourceRelative, mustRead(t, sourceAbsolute))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(document.Body, body) {
		t.Fatal("source body changed during commit")
	}
}

func TestCommitRefusesChangedHeadAndTamperedArtifact(t *testing.T) {
	t.Run("changed_head", func(t *testing.T) {
		repo := transactionTestRepository(t)
		service := core.NewService(repo)
		service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
		service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
		page := validTransactionPage("page_head", "Head", "2026-07-28", "Head.\n")
		preview, err := service.Preview(context.Background(), transactionRequest(t, "create: head", []map[string]any{{
			"op": "create_page", "path": "pages/head.md", "content": string(page),
		}}))
		if err != nil {
			t.Fatal(err)
		}
		fixture := filepath.Join(repo.Root, "system", "head-change.txt")
		if err := os.WriteFile(fixture, []byte("change"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo.Root, "add", "--", "system/head-change.txt")
		runGit(t, repo.Root, "commit", "-m", "maintenance: advance head")
		_, err = service.Commit(context.Background(), core.CommitOptions{
			TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest,
		})
		var apiErr *core.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != "base_commit_changed" || apiErr.ExitCode != core.ExitConflict {
			t.Fatalf("error = %#v", err)
		}
		if _, err := os.Stat(filepath.Join(repo.Root, "pages", "head.md")); !os.IsNotExist(err) {
			t.Fatalf("changed-head commit touched target: %v", err)
		}
	})

	t.Run("tampered_diff", func(t *testing.T) {
		repo := transactionTestRepository(t)
		service := core.NewService(repo)
		service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
		service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
		page := validTransactionPage("page_tamper", "Tamper", "2026-07-28", "Tamper.\n")
		preview, err := service.Preview(context.Background(), transactionRequest(t, "create: tamper", []map[string]any{{
			"op": "create_page", "path": "pages/tamper.md", "content": string(page),
		}}))
		if err != nil {
			t.Fatal(err)
		}
		diffPath := filepath.Join(repo.Root, ".lore", "transactions", preview.TransactionID, "diff.patch")
		if err := os.WriteFile(diffPath, []byte("tampered"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = service.Commit(context.Background(), core.CommitOptions{
			TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest,
		})
		var apiErr *core.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != "transaction_integrity_failed" {
			t.Fatalf("error = %#v", err)
		}
		if _, err := os.Stat(filepath.Join(repo.Root, "pages", "tamper.md")); !os.IsNotExist(err) {
			t.Fatalf("tampered commit touched target: %v", err)
		}
	})
}

func TestCommitFailureRollsBackExactFilesAndIndex(t *testing.T) {
	repo := transactionTestRepository(t)
	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	page := validTransactionPage("page_rollback", "Rollback", "2026-07-28", "Rollback.\n")
	preview, err := service.Preview(context.Background(), transactionRequest(t, "create: rollback", []map[string]any{{
		"op": "create_page", "path": "pages/rollback.md", "content": string(page),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	blankConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(blankConfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", blankConfig)
	_, err = service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest,
	})
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "git_commit_failed" {
		t.Fatalf("error = %#v", err)
	}
	if rolledBack, _ := apiErr.Details["rolled_back"].(bool); !rolledBack {
		t.Fatalf("error does not report rollback: %+v", apiErr)
	}
	if _, err := os.Stat(filepath.Join(repo.Root, "pages", "rollback.md")); !os.IsNotExist(err) {
		t.Fatalf("created file was not rolled back: %v", err)
	}
	if status := runGit(t, repo.Root, "status", "--porcelain=v1", "--", "pages/rollback.md"); status != "" {
		t.Fatalf("target remains in Git index: %q", status)
	}
	if _, err := os.Stat(filepath.Join(repo.Root, ".lore", "recovery", "active")); !os.IsNotExist(err) {
		t.Fatalf("recovery journal remains after rollback: %v", err)
	}
	shown, err := service.TransactionShow(preview.TransactionID, false)
	if err != nil {
		t.Fatal(err)
	}
	if shown.State.Status != "failed" {
		t.Fatalf("state = %+v", shown.State)
	}
}

func TestCommitPushFailureKeepsLocalCommit(t *testing.T) {
	repo := transactionTestRepository(t)
	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	page := validTransactionPage("page_push", "Push", "2026-07-28", "Push.\n")
	preview, err := service.Preview(context.Background(), transactionRequest(t, "create: push", []map[string]any{{
		"op": "create_page", "path": "pages/push.md", "content": string(page),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	push := true
	result, err := service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest, Push: &push,
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if result.Pushed || result.Commit == "" || len(result.Warnings) == 0 {
		t.Fatalf("result = %+v", result)
	}
	if head := strings.TrimSpace(runGit(t, repo.Root, "rev-parse", "HEAD")); head != result.Commit {
		t.Fatalf("local HEAD = %s, commit = %s", head, result.Commit)
	}
}

func TestCommitPushesToLocalBareRemote(t *testing.T) {
	repo := transactionTestRepository(t)
	bare := filepath.Join(t.TempDir(), "remote.git")
	command := exec.Command("git", "init", "--bare", "--initial-branch=main", bare)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, output)
	}
	runGit(t, repo.Root, "remote", "add", "origin", bare)
	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	page := validTransactionPage("page_pushed", "Pushed", "2026-07-28", "Pushed.\n")
	preview, err := service.Preview(context.Background(), transactionRequest(t, "create: pushed", []map[string]any{{
		"op": "create_page", "path": "pages/pushed.md", "content": string(page),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	push := true
	result, err := service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest, Push: &push,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Pushed {
		t.Fatalf("result = %+v", result)
	}
	remoteCommand := exec.Command("git", "--git-dir", bare, "rev-parse", "refs/heads/main")
	output, err := remoteCommand.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(output)) != result.Commit {
		t.Fatalf("remote head = %q, commit = %q", output, result.Commit)
	}
}

func TestCommitRequiredPushFailureReportsSafeLocalCommit(t *testing.T) {
	repo := transactionTestRepository(t)
	repo.Config.Git.RequirePush = true
	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	page := validTransactionPage("page_required_push", "Required push", "2026-07-28", "Required.\n")
	preview, err := service.Preview(context.Background(), transactionRequest(t, "create: required push", []map[string]any{{
		"op": "create_page", "path": "pages/required-push.md", "content": string(page),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	push := true
	result, err := service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest, Push: &push,
	})
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "push_required_failed" || apiErr.ExitCode != core.ExitRuntime {
		t.Fatalf("error = %#v", err)
	}
	if result.Commit == "" || strings.TrimSpace(runGit(t, repo.Root, "rev-parse", "HEAD")) != result.Commit {
		t.Fatalf("local commit was not retained: %+v", result)
	}
}

func transactionTestRepository(t *testing.T) *repository.Repository {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(globalConfig, []byte("[user]\n\tname = Lore Test\n\temail = lore@example.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := filepath.Join(t.TempDir(), "knowledge")
	if _, err := initrepo.Initialize(context.Background(), initrepo.Options{Path: root}, gitx.New()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func validTransactionPage(id, title, updated, body string) []byte {
	return []byte(`---
id: ` + id + `
title: ` + title + `
kind: topic
created: "2026-07-27"
updated: "` + updated + `"
status: active
sensitivity: normal
---
` + body)
}

func transactionRequest(t *testing.T, message string, operations []map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"schema_version": 1,
		"message":        message,
		"operations":     operations,
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
