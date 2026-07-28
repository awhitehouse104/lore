package core_test

import (
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
