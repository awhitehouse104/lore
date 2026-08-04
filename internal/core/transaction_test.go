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
	loreindex "lore/internal/index"
	"lore/internal/initrepo"
	"lore/internal/lock"
	"lore/internal/repository"
	"lore/internal/search"
)

const fixedTransactionID = "tx_01ARZ3NDEKTSV4RRFFQ69G5FAV"

type fixedTransactionIDs struct {
	value string
}

func (g fixedTransactionIDs) New(time.Time) (string, error) {
	return g.value, nil
}

type transactionFailHooks struct {
	fileIndex int
	afterGit  bool
}

func (h transactionFailHooks) AfterFileRename(index int, _ string) error {
	if index == h.fileIndex {
		return errors.New("injected after file rename")
	}
	return nil
}

func (h transactionFailHooks) AfterGitCommit(string) error {
	if h.afterGit {
		return errors.New("injected after Git commit")
	}
	return nil
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
	discarded, err := service.TransactionDiscard(context.Background(), fixedTransactionID)
	if err != nil {
		t.Fatalf("TransactionDiscard: %v", err)
	}
	if discarded.Status != "discarded" {
		t.Fatalf("discarded = %+v", discarded)
	}
	if _, err := service.TransactionDiscard(context.Background(), fixedTransactionID); err != nil {
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

func TestReferencesAndAtomicRecipeReorganization(t *testing.T) {
	repo := transactionTestRepository(t)
	oldPage := validTransactionPage("page_old_recipe", "Old Recipe", "2026-07-27", "Old recipe.\n")
	backlink := validTransactionPage("page_meal_plan", "Meal Plan", "2026-07-27", "Make [the old recipe](old-recipe.md#method).\n")
	if err := os.WriteFile(filepath.Join(repo.Root, "pages", "old-recipe.md"), oldPage, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.Root, "pages", "meal-plan.md"), backlink, 0o644); err != nil {
		t.Fatal(err)
	}
	sourceBody := []byte("Historical note: [Old Recipe](../../../pages/old-recipe.md).")
	source := docs.Source{
		ID:             fixedID,
		Kind:           "user_statement",
		CapturedAt:     "2026-07-28T18:00:00Z",
		Origin:         "test",
		RawSHA256:      docs.SHA256(sourceBody),
		Sensitivity:    "normal",
		IntegratedAt:   "2026-07-28T18:05:00Z",
		IntegratedInto: []string{"page_old_recipe"},
	}
	sourceData, err := docs.MarshalSource(source, sourceBody)
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
	runGit(t, repo.Root, "add", "--", "pages/old-recipe.md", "pages/meal-plan.md", sourceRelative)
	runGit(t, repo.Root, "commit", "-m", "maintenance: recipe fixtures")

	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	references, err := service.PageReferences(context.Background(), "page_old_recipe")
	if err != nil {
		t.Fatal(err)
	}
	if len(references.LiveBacklinks) != 1 || references.LiveBacklinks[0].Path != "pages/meal-plan.md" ||
		len(references.HistoricalSourceMentions) != 1 || references.HistoricalSourceMentions[0].Path != sourceRelative ||
		len(references.SourceIntegrations) != 1 || references.SourceIntegrations[0].Path != sourceRelative {
		t.Fatalf("references = %+v", references)
	}

	newPage := validTransactionPage("page_lemon_recipe", "Lemon Recipe", "2026-07-28", "New recipe.\n")
	updatedBacklink := validTransactionPage("page_meal_plan", "Meal Plan", "2026-07-28", "Make [the lemon recipe](lemon-recipe.md#method).\n")
	preview, err := service.Preview(context.Background(), transactionRequest(t, "maintenance: reorganize recipes", []map[string]any{
		{"op": "create_page", "path": "pages/lemon-recipe.md", "content": string(newPage)},
		{"op": "update_page", "path": "pages/meal-plan.md", "expected_revision": docs.Revision(backlink), "content": string(updatedBacklink)},
		{"op": "mark_source_integrated", "path": sourceRelative, "expected_revision": docs.Revision(sourceData), "page_ids": []string{"page_lemon_recipe"}},
		{"op": "delete_page", "path": "pages/old-recipe.md", "expected_revision": docs.Revision(oldPage)},
	}))
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if !preview.Lint.Valid || !strings.Contains(preview.Diff, "+++ /dev/null") ||
		!strings.Contains(preview.Diff, "pages/lemon-recipe.md") {
		t.Fatalf("preview = %+v", preview)
	}
	if _, err := service.TransactionShowOwned(context.Background(), preview.TransactionID, true, search.AllAccessPolicy()); err != nil {
		t.Fatalf("TransactionShowOwned for deletion preview: %v", err)
	}
	committed, err := service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID,
		PreviewDigest: preview.PreviewDigest,
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(committed.ChangedPaths) != 4 {
		t.Fatalf("commit = %+v", committed)
	}
	if _, err := os.Stat(filepath.Join(repo.Root, "pages", "old-recipe.md")); !os.IsNotExist(err) {
		t.Fatalf("old recipe still exists: %v", err)
	}
	if got := string(mustRead(t, filepath.Join(repo.Root, "pages", "meal-plan.md"))); !strings.Contains(got, "lemon-recipe.md#method") {
		t.Fatalf("backlink was not updated: %s", got)
	}
	updatedSource, err := docs.Parse(sourceRelative, mustRead(t, sourceAbsolute))
	if err != nil {
		t.Fatal(err)
	}
	if string(updatedSource.Body) != string(sourceBody) ||
		!containsValue(updatedSource.Source.IntegratedInto, "page_old_recipe") ||
		!containsValue(updatedSource.Source.IntegratedInto, "page_lemon_recipe") {
		t.Fatalf("source history changed incorrectly: %+v body=%q", updatedSource.Source, updatedSource.Body)
	}
}

func TestPreviewAllowsPageIDChangeWithCurrentUpdateDate(t *testing.T) {
	repo := transactionTestRepository(t)
	current := validTransactionPage("page_old_id", "Recipe", "2026-07-27", "Recipe.\n")
	path := filepath.Join(repo.Root, "pages", "recipe.md")
	if err := os.WriteFile(path, current, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.Root, "add", "--", "pages/recipe.md")
	runGit(t, repo.Root, "commit", "-m", "maintenance: recipe fixture")
	proposed := validTransactionPage("page_new_id", "Recipe", "2026-07-28", "Recipe.\n")
	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	preview, err := service.Preview(context.Background(), transactionRequest(t, "maintenance: rekey recipe", []map[string]any{{
		"op": "update_page", "path": "pages/recipe.md", "expected_revision": docs.Revision(current), "content": string(proposed),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview.Diff, "-id: page_old_id") || !strings.Contains(preview.Diff, "+id: page_new_id") {
		t.Fatalf("rekey diff = %s", preview.Diff)
	}
	if _, err := service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID,
		PreviewDigest: preview.PreviewDigest,
	}); err != nil {
		t.Fatalf("Commit rekey: %v", err)
	}
	updated, err := docs.Parse("pages/recipe.md", mustRead(t, path))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Page.ID != "page_new_id" {
		t.Fatalf("committed page ID = %q", updated.Page.ID)
	}
}

func TestPreviewDeleteRequiresLivePageBacklinksToBeRepaired(t *testing.T) {
	repo := transactionTestRepository(t)
	target := validTransactionPage("page_delete_target", "Delete Target", "2026-07-27", "Target.\n")
	backlink := validTransactionPage("page_delete_backlink", "Delete Backlink", "2026-07-27", "See [target](delete-target.md).\n")
	if err := os.WriteFile(filepath.Join(repo.Root, "pages", "delete-target.md"), target, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.Root, "pages", "delete-backlink.md"), backlink, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.Root, "add", "--", "pages/delete-target.md", "pages/delete-backlink.md")
	runGit(t, repo.Root, "commit", "-m", "maintenance: deletion fixtures")

	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	_, err := service.Preview(context.Background(), transactionRequest(t, "maintenance: delete linked page", []map[string]any{{
		"op": "delete_page", "path": "pages/delete-target.md", "expected_revision": docs.Revision(target),
	}}))
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "prospective_lint_invalid" {
		t.Fatalf("Preview error = %v", err)
	}
}

func TestSetSourceSensitivityPreviewCommitAndDowngradeConfirmation(t *testing.T) {
	repo := transactionTestRepository(t)
	body := []byte("exact\r\nprivate source body")
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
	runGit(t, repo.Root, "add", "--", sourceRelative)
	runGit(t, repo.Root, "commit", "-m", "maintenance: sensitivity fixture")

	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	preview, err := service.Preview(context.Background(), transactionRequest(t, "correct: source sensitivity", []map[string]any{{
		"op": "set_source_sensitivity", "path": sourceRelative,
		"expected_revision": docs.Revision(sourceData), "sensitivity": "sensitive",
	}}))
	if err != nil {
		t.Fatalf("Preview upgrade: %v", err)
	}
	if !strings.Contains(preview.Diff, "-sensitivity: normal") || !strings.Contains(preview.Diff, "+sensitivity: sensitive") {
		t.Fatalf("upgrade diff = %s", preview.Diff)
	}
	shown, err := service.TransactionShow(preview.TransactionID, false)
	if err != nil {
		t.Fatal(err)
	}
	operation := shown.Proposal.Operations[0]
	if operation.Sensitivity != "sensitive" || operation.AllowDowngrade {
		t.Fatalf("upgrade operation = %+v", operation)
	}
	result, err := service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest,
	})
	if err != nil {
		t.Fatalf("Commit upgrade: %v", err)
	}
	upgraded := mustRead(t, sourceAbsolute)
	document, err := docs.Parse(sourceRelative, upgraded)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "committed" || document.Source.Sensitivity != "sensitive" ||
		!bytes.Equal(document.Body, body) || document.Source.RawSHA256 != docs.SHA256(body) {
		t.Fatalf("upgraded result=%+v source=%+v", result, document.Source)
	}

	service.TxIDs = fixedTransactionIDs{value: "tx_01ARZ3NDEKTSV4RRFFQ69G5FAW"}
	downgrade := map[string]any{
		"op": "set_source_sensitivity", "path": sourceRelative,
		"expected_revision": docs.Revision(upgraded), "sensitivity": "normal",
	}
	_, err = service.Preview(context.Background(), transactionRequest(t, "correct: source sensitivity", []map[string]any{downgrade}))
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "sensitivity_downgrade_requires_confirmation" {
		t.Fatalf("unconfirmed downgrade error = %#v", err)
	}
	downgrade["allow_downgrade"] = true
	preview, err = service.Preview(context.Background(), transactionRequest(t, "correct: source sensitivity", []map[string]any{downgrade}))
	if err != nil {
		t.Fatalf("Preview downgrade: %v", err)
	}
	shown, err = service.TransactionShow(preview.TransactionID, false)
	if err != nil {
		t.Fatal(err)
	}
	if !shown.Proposal.Operations[0].AllowDowngrade {
		t.Fatalf("downgrade confirmation was not retained: %+v", shown.Proposal.Operations[0])
	}
	if _, err := service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest,
	}); err != nil {
		t.Fatalf("Commit downgrade: %v", err)
	}
	downgraded, err := docs.Parse(sourceRelative, mustRead(t, sourceAbsolute))
	if err != nil {
		t.Fatal(err)
	}
	if downgraded.Source.Sensitivity != "normal" || !bytes.Equal(downgraded.Body, body) {
		t.Fatalf("downgraded source = %+v", downgraded.Source)
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

func TestPreviewEnforcesImmutableCreatedAndCurrentUpdateDate(t *testing.T) {
	tests := []struct {
		name      string
		transform func([]byte) []byte
		wantCode  string
		minimum   string
	}{
		{
			name: "no_effect",
			transform: func(data []byte) []byte {
				return data
			},
			wantCode: "operation_has_no_effect",
		},
		{
			name: "created",
			transform: func(data []byte) []byte {
				return bytes.Replace(data, []byte(`created: "2026-07-27"`), []byte(`created: "2026-07-26"`), 1)
			},
			wantCode: "immutable_page_created",
		},
		{
			name: "old_updated_for_body_change",
			transform: func(data []byte) []byte {
				return bytes.Replace(data, []byte("Original."), []byte("Changed."), 1)
			},
			wantCode: "updated_too_old",
			minimum:  "2026-07-28",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := transactionTestRepository(t)
			current := validTransactionPage("page_existing", "Existing", "2026-07-27", "Original.\n")
			path := filepath.Join(repo.Root, "pages", "existing.md")
			if err := os.WriteFile(path, current, 0o644); err != nil {
				t.Fatal(err)
			}
			runGit(t, repo.Root, "add", "--", "pages/existing.md")
			runGit(t, repo.Root, "commit", "-m", "maintenance: fixture")
			service := core.NewService(repo)
			service.Clock = fixedClock{value: time.Date(2026, 7, 27, 21, 10, 0, 0, time.FixedZone("EDT", -4*60*60))}
			service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
			proposed := tt.transform(append([]byte(nil), current...))
			_, err := service.Preview(context.Background(), transactionRequest(t, "update: immutable check", []map[string]any{{
				"op": "update_page", "path": "pages/existing.md",
				"expected_revision": docs.Revision(current), "content": string(proposed),
			}}))
			var apiErr *core.APIError
			if !errors.As(err, &apiErr) || apiErr.Code != tt.wantCode {
				t.Fatalf("error = %#v, want %s", err, tt.wantCode)
			}
			if tt.minimum != "" {
				if apiErr.Details["field"] != "updated" ||
					apiErr.Details["minimum"] != tt.minimum ||
					apiErr.Details["path"] != "pages/existing.md" {
					t.Fatalf("error details = %#v", apiErr.Details)
				}
			}
		})
	}
}

func TestCommitRefusesStaleTargetWithoutOverwritingIt(t *testing.T) {
	repo := transactionTestRepository(t)
	current := validTransactionPage("page_stale_target", "Stale target", "2026-07-27", "Original.\n")
	path := filepath.Join(repo.Root, "pages", "stale-target.md")
	if err := os.WriteFile(path, current, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.Root, "add", "--", "pages/stale-target.md")
	runGit(t, repo.Root, "commit", "-m", "maintenance: fixture")
	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	proposed := validTransactionPage("page_stale_target", "Stale target", "2026-07-28", "Proposed.\n")
	preview, err := service.Preview(context.Background(), transactionRequest(t, "update: stale target", []map[string]any{{
		"op": "update_page", "path": "pages/stale-target.md",
		"expected_revision": docs.Revision(current), "content": string(proposed),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	external := bytes.Replace(current, []byte("Original."), []byte("External."), 1)
	if err := os.WriteFile(path, external, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest,
	})
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.ExitCode != core.ExitConflict {
		t.Fatalf("error = %#v", err)
	}
	if got := mustRead(t, path); !bytes.Equal(got, external) {
		t.Fatalf("stale target was overwritten: %q", got)
	}
	if _, err := os.Stat(filepath.Join(repo.Root, ".lore", "recovery", "active")); !os.IsNotExist(err) {
		t.Fatalf("stale conflict created recovery journal: %v", err)
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
	unrelatedBody := []byte("unrelated source body")
	unrelatedSource := docs.Source{
		ID:          "src_01ARZ3NDEKTSV4RRFFQ69G5FAW",
		Kind:        "note",
		CapturedAt:  "2026-07-28T18:30:00Z",
		Origin:      "test",
		OriginRef:   "before",
		RawSHA256:   docs.SHA256(unrelatedBody),
		Sensitivity: "normal",
	}
	unrelatedData, err := docs.MarshalSource(unrelatedSource, unrelatedBody)
	if err != nil {
		t.Fatal(err)
	}
	unrelatedRelative := "sources/2026/07/" + unrelatedSource.ID + "-note.md"
	unrelatedAbsolute := filepath.Join(repo.Root, filepath.FromSlash(unrelatedRelative))
	if err := os.WriteFile(unrelatedAbsolute, unrelatedData, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.Root, "add", "--", "pages/first.md", "pages/second.md", sourceRelative, unrelatedRelative)
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
	unrelatedChanged := bytes.Replace(unrelatedData, []byte("origin_ref: before"), []byte("origin_ref: after"), 1)
	if bytes.Equal(unrelatedChanged, unrelatedData) {
		t.Fatal("unrelated source fixture did not change")
	}
	if err := os.WriteFile(unrelatedAbsolute, unrelatedChanged, 0o644); err != nil {
		t.Fatal(err)
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
	unrelatedBefore := runGit(t, repo.Root, "status", "--porcelain=v1", "-z", "--", "system/OPERATING_RULES.md", "README.md", unrelatedRelative)

	result, err := service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest,
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(result.Warnings) != 1 || !strings.HasPrefix(result.Warnings[0], "uncommitted_source_change: "+unrelatedRelative+":") {
		t.Fatalf("commit warnings = %v", result.Warnings)
	}
	paths, err := gitx.New().ChangedPathsInCommit(context.Background(), repo.Root, result.Commit)
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"pages/first.md", "pages/second.md", sourceRelative}
	if strings.Join(paths, "\n") != strings.Join(expected, "\n") {
		t.Fatalf("commit paths = %v, want %v", paths, expected)
	}
	unrelatedAfter := runGit(t, repo.Root, "status", "--porcelain=v1", "-z", "--", "system/OPERATING_RULES.md", "README.md", unrelatedRelative)
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

func TestCommitRefreshFailureKeepsCanonicalCommit(t *testing.T) {
	repo := transactionTestRepository(t)
	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	maintenance := &fakeIndexMaintenance{
		status:    loreindex.Status{IndexState: loreindex.StateStale},
		updateErr: errors.New("database unavailable"),
	}
	service.IndexMaintenance = maintenance
	page := validTransactionPage("page_refresh_failure", "Refresh failure", "2026-07-29", "Durable.\n")
	preview, err := service.Preview(context.Background(), transactionRequest(t, "create: refresh failure", []map[string]any{{
		"op": "create_page", "path": "pages/refresh-failure.md", "content": string(page),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest,
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if result.Commit == "" || maintenance.updateCalls != 1 {
		t.Fatalf("result=%+v refresh=%+v", result, maintenance)
	}
	foundWarning := false
	for _, warning := range result.Warnings {
		if warning == "existing index refresh failed; run lore index update" {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatalf("missing refresh warning: %v", result.Warnings)
	}
	if _, err := os.Stat(filepath.Join(repo.Root, "pages", "refresh-failure.md")); err != nil {
		t.Fatalf("canonical page was undone: %v", err)
	}
	if strings.TrimSpace(runGit(t, repo.Root, "rev-parse", "HEAD")) != result.Commit {
		t.Fatalf("canonical Git commit was undone: %+v", result)
	}
}

func TestCommitRefreshesExistingGitIndex(t *testing.T) {
	repo := transactionTestRepository(t)
	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	if _, err := service.IndexBuild(context.Background(), core.IndexBuildOptions{}); err != nil {
		t.Fatalf("IndexBuild: %v", err)
	}
	page := validTransactionPage("page_index_refresh", "Index refresh", "2026-07-29", "Indexed.\n")
	preview, err := service.Preview(context.Background(), transactionRequest(t, "create: index refresh", []map[string]any{{
		"op": "create_page", "path": "pages/index-refresh.md", "content": string(page),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest,
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("successful index refresh warnings = %v", result.Warnings)
	}
	status, err := service.IndexStatus(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if status.IndexState != loreindex.StateFresh || status.PageCount != 1 {
		t.Fatalf("index status = %+v", status)
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

func TestRecoveryRollbackAfterInjectedFileRename(t *testing.T) {
	repo := transactionTestRepository(t)
	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	service.TxHooks = transactionFailHooks{fileIndex: 0}
	page := validTransactionPage("page_interrupted", "Interrupted", "2026-07-28", "Interrupted.\n")
	preview, err := service.Preview(context.Background(), transactionRequest(t, "create: interrupted", []map[string]any{{
		"op": "create_page", "path": "pages/interrupted.md", "content": string(page),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest,
	})
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "injected_interruption" {
		t.Fatalf("error = %#v", err)
	}
	if got := mustRead(t, filepath.Join(repo.Root, "pages", "interrupted.md")); !bytes.Equal(got, page) {
		t.Fatal("interrupted apply did not leave exact proposed bytes")
	}
	status, err := service.RecoveryStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Active || status.RecommendedAction != "lore recover --rollback" {
		t.Fatalf("status = %+v", status)
	}
	if _, err := service.Capture(context.Background(), core.CaptureOptions{
		Kind: "user_statement", Origin: "test", Sensitivity: "normal", Body: []byte("blocked capture"), NoCommit: true,
	}); !errors.As(err, &apiErr) || apiErr.Code != "recovery_required" {
		t.Fatalf("capture was not blocked by recovery: %#v", err)
	}
	service.TxHooks = nil
	result, err := service.RollbackRecovery(context.Background())
	if err != nil {
		t.Fatalf("RollbackRecovery: %v", err)
	}
	if result.Status != "failed" || !result.Lint.Valid {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(repo.Root, "pages", "interrupted.md")); !os.IsNotExist(err) {
		t.Fatalf("rollback did not remove created page: %v", err)
	}
	status, err = service.RecoveryStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Active || status.RecommendedAction != "none" {
		t.Fatalf("status after rollback = %+v", status)
	}
}

func TestRecoveryRollbackRestoresInterruptedDeletion(t *testing.T) {
	repo := transactionTestRepository(t)
	page := validTransactionPage("page_delete_rollback", "Delete rollback", "2026-07-28", "Keep me.\n")
	path := filepath.Join(repo.Root, "pages", "delete-rollback.md")
	if err := os.WriteFile(path, page, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.Root, "add", "--", "pages/delete-rollback.md")
	runGit(t, repo.Root, "commit", "-m", "maintenance: deletion fixture")
	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	service.TxHooks = transactionFailHooks{fileIndex: 0}
	preview, err := service.Preview(context.Background(), transactionRequest(t, "archive: interrupted deletion", []map[string]any{{
		"op": "delete_page", "path": "pages/delete-rollback.md", "expected_revision": docs.Revision(page),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest,
	})
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "injected_interruption" {
		t.Fatalf("error = %#v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("interrupted deletion left page present: %v", err)
	}
	service.TxHooks = nil
	if _, err := service.RollbackRecovery(context.Background()); err != nil {
		t.Fatalf("RollbackRecovery: %v", err)
	}
	if restored := mustRead(t, path); !bytes.Equal(restored, page) {
		t.Fatalf("restored page = %q", restored)
	}
}

func TestRecoveryFinalizeRecognizesCommittedDeletion(t *testing.T) {
	repo := transactionTestRepository(t)
	page := validTransactionPage("page_delete_finalize", "Delete finalize", "2026-07-28", "Delete me.\n")
	path := filepath.Join(repo.Root, "pages", "delete-finalize.md")
	if err := os.WriteFile(path, page, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.Root, "add", "--", "pages/delete-finalize.md")
	runGit(t, repo.Root, "commit", "-m", "maintenance: deletion fixture")
	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	service.TxHooks = transactionFailHooks{fileIndex: -1, afterGit: true}
	preview, err := service.Preview(context.Background(), transactionRequest(t, "archive: finalized deletion", []map[string]any{{
		"op": "delete_page", "path": "pages/delete-finalize.md", "expected_revision": docs.Revision(page),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest,
	})
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "injected_interruption" {
		t.Fatalf("error = %#v", err)
	}
	service.TxHooks = nil
	result, err := service.FinalizeRecovery(context.Background())
	if err != nil {
		t.Fatalf("FinalizeRecovery: %v", err)
	}
	if result.Status != "committed" || result.Commit == "" {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("finalized deletion restored page: %v", err)
	}
}

func TestRecoveryFinalizeAfterInjectedGitCommit(t *testing.T) {
	repo := transactionTestRepository(t)
	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	service.TxHooks = transactionFailHooks{fileIndex: -1, afterGit: true}
	page := validTransactionPage("page_finalize", "Finalize", "2026-07-28", "Finalize.\n")
	preview, err := service.Preview(context.Background(), transactionRequest(t, "create: finalize", []map[string]any{{
		"op": "create_page", "path": "pages/finalize.md", "content": string(page),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(runGit(t, repo.Root, "rev-parse", "HEAD"))
	_, err = service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest,
	})
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "injected_interruption" {
		t.Fatalf("error = %#v", err)
	}
	commitHash := strings.TrimSpace(runGit(t, repo.Root, "rev-parse", "HEAD"))
	if commitHash == base {
		t.Fatal("injected interruption occurred before Git commit")
	}
	status, err := service.RecoveryStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Active || status.RecommendedAction != "lore recover --finalize" || status.Commit != commitHash {
		t.Fatalf("status = %+v", status)
	}
	service.TxHooks = nil
	if _, err := service.RollbackRecovery(context.Background()); err == nil {
		t.Fatal("rollback was allowed after the exact Git commit existed")
	} else if !errors.As(err, &apiErr) || apiErr.Code != "recovery_finalize_required" {
		t.Fatalf("rollback error = %#v", err)
	}
	if head := strings.TrimSpace(runGit(t, repo.Root, "rev-parse", "HEAD")); head != commitHash {
		t.Fatal("refused rollback changed the canonical Git commit")
	}
	result, err := service.FinalizeRecovery(context.Background())
	if err != nil {
		t.Fatalf("FinalizeRecovery: %v", err)
	}
	if result.Status != "committed" || result.Commit != commitHash {
		t.Fatalf("result = %+v", result)
	}
	if head := strings.TrimSpace(runGit(t, repo.Root, "rev-parse", "HEAD")); head != commitHash {
		t.Fatal("finalize changed the canonical Git commit")
	}
	repeated, err := service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest,
	})
	if err != nil || !repeated.AlreadyCommitted || repeated.Commit != commitHash {
		t.Fatalf("idempotent commit after finalize = %+v, %v", repeated, err)
	}
}

func TestRecoveryRollbackRefusesUnexpectedExternalEdit(t *testing.T) {
	repo := transactionTestRepository(t)
	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	service.TxHooks = transactionFailHooks{fileIndex: 0}
	page := validTransactionPage("page_external", "External", "2026-07-28", "Proposed.\n")
	preview, err := service.Preview(context.Background(), transactionRequest(t, "create: external", []map[string]any{{
		"op": "create_page", "path": "pages/external.md", "content": string(page),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest,
	})
	if err == nil {
		t.Fatal("injected interruption did not fire")
	}
	external := []byte("third-party edit")
	path := filepath.Join(repo.Root, "pages", "external.md")
	if err := os.WriteFile(path, external, 0o644); err != nil {
		t.Fatal(err)
	}
	service.TxHooks = nil
	_, err = service.RollbackRecovery(context.Background())
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "rollback_conflict" || apiErr.ExitCode != core.ExitConflict {
		t.Fatalf("error = %#v", err)
	}
	if got := mustRead(t, path); !bytes.Equal(got, external) {
		t.Fatalf("external edit was overwritten: %q", got)
	}
	shown, err := service.TransactionShow(preview.TransactionID, false)
	if err != nil {
		t.Fatal(err)
	}
	if shown.State.Status != "recovery_required" {
		t.Fatalf("state = %+v", shown.State)
	}
}

func TestTransactionWritersRespectRepositoryLock(t *testing.T) {
	repo := transactionTestRepository(t)
	service := core.NewService(repo)
	service.WriteLockWait = 0
	now := time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)
	service.Clock = fixedClock{value: now}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	page := validTransactionPage("page_locked", "Locked", "2026-07-28", "Locked.\n")
	request := transactionRequest(t, "create: locked", []map[string]any{{
		"op": "create_page", "path": "pages/locked.md", "content": string(page),
	}})
	handle, err := lock.Acquire(context.Background(), repo.Root, "test holder", now, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Preview(context.Background(), request)
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "repository_locked" {
		t.Fatalf("preview lock error = %#v", err)
	}
	if err := handle.Release(); err != nil {
		t.Fatal(err)
	}
	preview, err := service.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	handle, err = lock.Acquire(context.Background(), repo.Root, "test holder", now, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Release()
	_, err = service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID, PreviewDigest: preview.PreviewDigest,
	})
	if !errors.As(err, &apiErr) || apiErr.Code != "repository_locked" {
		t.Fatalf("commit lock error = %#v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.Root, "pages", "locked.md")); !os.IsNotExist(err) {
		t.Fatalf("locked commit touched target: %v", err)
	}
}

func TestContextCancellationInterruptsRepositoryWriterWaits(t *testing.T) {
	repo := transactionTestRepository(t)
	service := core.NewService(repo)
	now := time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)
	service.Clock = fixedClock{value: now}
	handle, err := lock.Acquire(context.Background(), repo.Root, "test holder", now, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Release()

	tests := []struct {
		name string
		run  func(context.Context) error
	}{
		{
			name: "index clear",
			run: func(ctx context.Context) error {
				_, err := service.IndexClear(ctx)
				return err
			},
		},
		{
			name: "transaction discard",
			run: func(ctx context.Context) error {
				_, err := service.TransactionDiscard(ctx, fixedTransactionID)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() {
				result <- test.run(ctx)
			}()
			time.Sleep(30 * time.Millisecond)
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("writer error = %T %v, want context cancellation", err, err)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("writer did not stop after context cancellation")
			}
		})
	}
}

func TestCommitRequiresMatchingDigestAndLocalActor(t *testing.T) {
	repo := transactionTestRepository(t)
	service := core.NewService(repo)
	service.Clock = fixedClock{value: time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC)}
	service.TxIDs = fixedTransactionIDs{value: fixedTransactionID}
	page := validTransactionPage("page_identity", "Identity", "2026-07-28", "Identity.\n")
	preview, err := service.Preview(context.Background(), transactionRequest(t, "create: identity", []map[string]any{{
		"op": "create_page", "path": "pages/identity.md", "content": string(page),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID,
		PreviewDigest: docs.SHA256([]byte("wrong")),
	})
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "preview_digest_mismatch" {
		t.Fatalf("digest error = %#v", err)
	}
	service.Actor = "other-actor"
	_, err = service.Commit(context.Background(), core.CommitOptions{
		TransactionID: preview.TransactionID,
		PreviewDigest: preview.PreviewDigest,
	})
	if !errors.As(err, &apiErr) || apiErr.Code != "actor_mismatch" {
		t.Fatalf("actor error = %#v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.Root, "pages", "identity.md")); !os.IsNotExist(err) {
		t.Fatalf("identity conflict touched target: %v", err)
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

func containsValue(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
