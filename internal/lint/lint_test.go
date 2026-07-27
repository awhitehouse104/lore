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
	"lore/internal/initrepo"
	"lore/internal/lint"
	"lore/internal/repository"
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
