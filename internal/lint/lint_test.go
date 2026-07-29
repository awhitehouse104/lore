package lint_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"lore/internal/docs"
	"lore/internal/gitx"
	loreindex "lore/internal/index"
	"lore/internal/initrepo"
	"lore/internal/lint"
	"lore/internal/recovery"
	"lore/internal/repository"
	"lore/internal/transaction"
)

const sourceID = "src_01ARZ3NDEKTSV4RRFFQ69G5FAV"

func TestLintIntegrityFindings(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge")
	if _, err := initrepo.Initialize(context.Background(), initrepo.Options{Path: root, NoGit: true}, gitx.New()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	first := []byte(`---
id: page_duplicate
title: First
kind: topic
aliases: [shared]
created: "2026-07-22"
updated: "2026-07-22"
status: active
sensitivity: normal
---
[Missing](missing.md)
[Escape](../../outside.md)
[External](https://example.com/ignored)
`)
	second := []byte(`---
id: page_duplicate
title: Second
kind: topic
aliases: [shared]
created: "2026-07-22"
updated: "2026-07-22"
status: active
sensitivity: normal
---
Second.
`)
	if err := os.WriteFile(filepath.Join(root, "pages", "first.md"), first, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pages", "second.md"), second, 0o644); err != nil {
		t.Fatal(err)
	}
	source := docs.Source{
		ID:          sourceID,
		Kind:        "user_statement",
		CapturedAt:  "2026-07-22T00:00:00Z",
		Origin:      "test",
		RawSHA256:   docs.SHA256([]byte("original")),
		Sensitivity: "normal",
	}
	data, err := docs.MarshalSource(source, []byte("tampered"))
	if err != nil {
		t.Fatal(err)
	}
	sourceDir := filepath.Join(root, "sources", "2025", "12")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, sourceID+"-user_statement.md"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := lint.Run(context.Background(), repo, gitx.New())
	if err != nil {
		t.Fatalf("lint.Run: %v", err)
	}
	if result.Valid {
		t.Fatalf("invalid repository reported valid: %+v", result)
	}
	required := []string{
		"ambiguous_page_name",
		"broken_link",
		"duplicate_id",
		"link_escapes_repository",
		"source_body_modified",
		"source_date_path_mismatch",
	}
	codes := map[string]bool{}
	for _, finding := range result.Findings {
		codes[finding.Code] = true
	}
	for _, code := range required {
		if !codes[code] {
			t.Errorf("missing finding %q in %+v", code, result.Findings)
		}
	}
}

func TestLintGitWarnings(t *testing.T) {
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
	source := docs.Source{
		ID:          sourceID,
		Kind:        "user_statement",
		CapturedAt:  docs.TimestampString(time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)),
		Origin:      "test",
		RawSHA256:   docs.SHA256([]byte("uncommitted")),
		Sensitivity: "normal",
	}
	data, err := docs.MarshalSource(source, []byte("uncommitted"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "sources", "2026", "07")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sourceID+"-user_statement.md"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "-C", root, "checkout", "--detach")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git checkout --detach: %v: %s", err, output)
	}

	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := lint.Run(context.Background(), repo, gitx.New())
	if err != nil {
		t.Fatalf("lint.Run: %v", err)
	}
	codes := map[string]bool{}
	for _, finding := range result.Findings {
		if finding.Severity == lint.SeverityWarning {
			codes[finding.Code] = true
		}
	}
	if !codes["uncommitted_source_change"] || !codes["detached_head"] {
		t.Fatalf("Git warning codes = %v, findings=%+v", codes, result.Findings)
	}
}

func TestLintReportsMissingManagedDirectoryWithoutRuntimeFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge")
	if _, err := initrepo.Initialize(context.Background(), initrepo.Options{Path: root, NoGit: true}, gitx.New()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "pages", ".gitkeep")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "pages")); err != nil {
		t.Fatal(err)
	}
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := lint.Run(context.Background(), repo, gitx.New())
	if err != nil {
		t.Fatalf("lint.Run: %v", err)
	}
	found := false
	for _, finding := range result.Findings {
		if finding.Code == "missing_required_directory" && finding.Path == "pages" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing pages finding absent: %+v", result.Findings)
	}
}

func TestLintOverlaySeesCreatedPageAndDoesNotTouchDisk(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge")
	if _, err := initrepo.Initialize(context.Background(), initrepo.Options{Path: root, NoGit: true}, gitx.New()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	sourceBody := []byte("source body")
	source := docs.Source{
		ID:             sourceID,
		Kind:           "user_statement",
		CapturedAt:     "2026-07-22T00:00:00Z",
		Origin:         "test",
		RawSHA256:      docs.SHA256(sourceBody),
		Sensitivity:    "normal",
		IntegratedAt:   "2026-07-22T01:00:00Z",
		IntegratedInto: []string{"page_new"},
	}
	sourceData, err := docs.MarshalSource(source, sourceBody)
	if err != nil {
		t.Fatal(err)
	}
	sourceDir := filepath.Join(root, "sources", "2026", "07")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, sourceID+"-user_statement.md"), sourceData, 0o644); err != nil {
		t.Fatal(err)
	}
	page := []byte(`---
id: page_new
title: New
kind: topic
created: "2026-07-22"
updated: "2026-07-22"
status: active
sensitivity: normal
---
New page.
`)
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	view, err := repository.NewOverlayView(repo, nil, map[string][]byte{"pages/new.md": page})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lint.RunView(context.Background(), repo, view, gitx.New())
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range result.Findings {
		if finding.Code == "integrated_page_missing" {
			t.Fatalf("overlay page reported missing: %+v", result.Findings)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "pages", "new.md")); !os.IsNotExist(err) {
		t.Fatalf("prospective page touched disk: %v", err)
	}
}

func TestLintRuntimeRecoveryAndStalePreviewFindings(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge")
	if _, err := initrepo.Initialize(context.Background(), initrepo.Options{Path: root, NoGit: true}, gitx.New()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	content := []byte(`---
id: page_stale
title: Stale
kind: topic
created: "2026-06-01"
updated: "2026-06-01"
status: active
sensitivity: normal
---
Stale.
`)
	diffBytes := []byte("diff")
	lintBytes := []byte("{}\n")
	proposal := transaction.Proposal{
		SchemaVersion: transaction.SchemaVersion,
		TransactionID: "tx_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		CreatedAt:     createdAt.Format(time.RFC3339Nano),
		BaseCommit:    "0123456789012345678901234567890123456789",
		BaseBranch:    "main",
		Actor:         transaction.DefaultActor,
		Message:       "create: stale",
		Operations: []transaction.EffectiveOperation{{
			Op:                     transaction.OperationCreatePage,
			Path:                   "pages/stale.md",
			ResultingContentSHA256: transaction.Digest(content),
			ContentFile:            "content/000.md",
		}},
		ChangedPaths: []string{"pages/stale.md"},
		DiffSHA256:   transaction.Digest(diffBytes),
		LintSHA256:   transaction.Digest(lintBytes),
	}
	transactionStore, err := transaction.NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := transactionStore.Save(transaction.Artifacts{
		Proposal: proposal,
		State: transaction.State{
			SchemaVersion: transaction.SchemaVersion,
			TransactionID: proposal.TransactionID,
			Status:        transaction.StatusPreviewed,
			UpdatedAt:     proposal.CreatedAt,
		},
		Diff: diffBytes, Lint: lintBytes, Contents: [][]byte{content},
	})
	if err != nil {
		t.Fatal(err)
	}
	recoveryStore, err := recovery.NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := recovery.NewJournal(
		proposal, digest, [][]byte{nil}, []bool{false}, createdAt, "commit",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := recoveryStore.Create(journal, [][]byte{nil}); err != nil {
		t.Fatal(err)
	}
	result, err := lint.RunAt(context.Background(), repo, gitx.New(), createdAt.Add(31*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]bool{}
	for _, finding := range result.Findings {
		codes[finding.Code] = true
	}
	if !codes["stale_transaction_preview"] || !codes["recovery_active"] || !result.Valid {
		t.Fatalf("runtime findings = %+v", result.Findings)
	}
}

func TestLintMalformedRecoveryJournalIsError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge")
	if _, err := initrepo.Initialize(context.Background(), initrepo.Options{Path: root, NoGit: true}, gitx.New()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	active := filepath.Join(root, ".lore", "recovery", "active")
	if err := os.MkdirAll(filepath.Join(active, "originals"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(active, "journal.json"), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := lint.RunAt(context.Background(), repo, gitx.New(), time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, finding := range result.Findings {
		if finding.Code == "malformed_recovery_journal" && finding.Severity == lint.SeverityError {
			found = true
		}
	}
	if !found || result.Valid {
		t.Fatalf("malformed recovery findings = %+v", result.Findings)
	}
}

func TestLintDerivedIndexWarningsDoNotInvalidateCanonicalRepository(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge")
	if _, err := initrepo.Initialize(context.Background(), initrepo.Options{Path: root, NoGit: true}, gitx.New()); err != nil {
		t.Fatal(err)
	}
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	manager := loreindex.NewManager(repo, gitx.New(), "0.3.0-test")
	if _, err := manager.Build(context.Background(), loreindex.BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, ".lore", "index.sqlite"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := lint.Run(context.Background(), repo, gitx.New())
	if err != nil {
		t.Fatal(err)
	}
	codes := findingCodes(result)
	if !result.Valid || !codes["index_uncertified"] || !codes["index_permissions_open"] {
		t.Fatalf("derived findings changed validity: %+v", result)
	}

	view, err := repository.NewOverlayView(repo, nil, map[string][]byte{})
	if err != nil {
		t.Fatal(err)
	}
	prospective, err := lint.RunView(context.Background(), repo, view, gitx.New())
	if err != nil {
		t.Fatal(err)
	}
	for code := range findingCodes(prospective) {
		if code == "index_uncertified" || code == "index_permissions_open" {
			t.Fatalf("prospective lint included derived finding %q: %+v", code, prospective.Findings)
		}
	}
}

func TestLintWarnsForStaleAndTrackedIndex(t *testing.T) {
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
		t.Fatal(err)
	}
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	manager := loreindex.NewManager(repo, gitx.New(), "0.3.0-test")
	if _, err := manager.Build(context.Background(), loreindex.BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, root, "add", "-f", "--", ".lore/index.sqlite")
	page := []byte(`---
id: page_lint_stale
title: Lint stale
kind: note
created: "2026-07-29"
updated: "2026-07-29"
status: active
sensitivity: normal
---
External canonical edit.
`)
	if err := os.WriteFile(filepath.Join(root, "pages", "lint-stale.md"), page, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := lint.Run(context.Background(), repo, gitx.New())
	if err != nil {
		t.Fatal(err)
	}
	codes := findingCodes(result)
	if !result.Valid || !codes["index_stale"] || !codes["index_file_tracked"] {
		t.Fatalf("stale/tracked findings = %+v", result)
	}
}

func TestLintWarnsForIndexSymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge")
	if _, err := initrepo.Initialize(context.Background(), initrepo.Options{Path: root, NoGit: true}, gitx.New()); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.sqlite")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".lore", "index.sqlite")); err != nil {
		t.Fatal(err)
	}
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := lint.Run(context.Background(), repo, gitx.New())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || !findingCodes(result)["index_path_symlink"] {
		t.Fatalf("symlink findings = %+v", result)
	}
}

func findingCodes(result lint.Result) map[string]bool {
	codes := make(map[string]bool, len(result.Findings))
	for _, finding := range result.Findings {
		codes[finding.Code] = true
	}
	return codes
}

func runGitCommand(t *testing.T, root string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", root}, args...)
	command := exec.Command("git", commandArgs...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
