package initrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"lore/internal/config"
	"lore/internal/gitx"
)

func TestInitializeFreshRepositoryAndIdempotent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge")
	options := Options{Path: root, NoGit: true}
	first, err := Initialize(context.Background(), options, gitx.New())
	if err != nil {
		t.Fatalf("Initialize first: %v", err)
	}
	if len(first.CreatedFiles) == 0 || len(first.ExistingFiles) != 0 {
		t.Fatalf("unexpected first result: %+v", first)
	}
	required := []string{
		"README.md", "AGENTS.md", "CLAUDE.md", "lore.yaml", ".gitignore",
		"pages/.gitkeep", "sources/.gitkeep", "assets/.gitkeep",
		"system/OPERATING_RULES.md", "system/PAGE_TEMPLATE.md", "system/SOURCE_TEMPLATE.md",
	}
	for _, relative := range required {
		info, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(relative)))
		if statErr != nil {
			t.Errorf("%s: %v", relative, statErr)
		} else if !info.Mode().IsRegular() {
			t.Errorf("%s is not a regular file", relative)
		}
	}
	guidanceChecks := []struct {
		path      string
		fragments []string
	}{
		{
			path: "AGENTS.md",
			fragments: []string{
				"authoritative shared policy",
				"repository-specific owner context",
				"compatibility passthrough",
				"normal knowledge maintenance",
				"Place repository-specific owner context",
			},
		},
		{
			path: "system/OPERATING_RULES.md",
			fragments: []string{
				"Lore intentionally supplies no default",
				"preserve enough of the verbatim exchange",
				"Link entity profiles to a shared subject",
				"living current view, not an append-only record",
				"CLI `lore references` or MCP `lore_page_references`",
				"Never rewrite an immutable source body",
				"additive historical ledger",
				"newly supplied ID must name a page present",
				"Complete preview and commit through the same actor and interface",
				"Prefer a revision-guarded `patch_page` operation",
				"use MCP `lore_read_many` for a bounded ordered batch",
				"preserve ambiguity or ask for clarification",
				"known user timezone",
				"preferred name",
				"only with the user's consent",
				"do not solicit unrelated personal information",
				"UTC metadata clock",
				"minimum` returned with `updated_too_old`",
				"local-full MCP `lore_preflight`",
				"every repository mutation or administrative operation",
				"routine verification and derived-index troubleshooting",
				"tool arguments to claim or override",
				"Never downgrade content known to be",
				"Use a client-generated key when automatic retries are possible",
			},
		},
	}
	for _, check := range guidanceChecks {
		data, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(check.path)))
		if readErr != nil {
			t.Fatalf("read generated %s: %v", check.path, readErr)
		}
		contents := strings.Join(strings.Fields(string(data)), " ")
		for _, fragment := range check.fragments {
			if !strings.Contains(contents, fragment) {
				t.Errorf("generated %s is missing %q", check.path, fragment)
			}
		}
	}
	agentsData, readErr := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if readErr != nil {
		t.Fatalf("read generated AGENTS.md: %v", readErr)
	}
	normalizedAgents := strings.Join(strings.Fields(string(agentsData)), " ")
	for _, duplicate := range []string{
		"Capture initiating raw information",
		"Use `lore preview`",
		"Idempotency keys are optional",
	} {
		if strings.Contains(normalizedAgents, duplicate) {
			t.Errorf("generated AGENTS.md duplicates shared policy %q", duplicate)
		}
	}
	claudeData, readErr := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if readErr != nil {
		t.Fatalf("read generated CLAUDE.md: %v", readErr)
	}
	const wantClaude = "# Claude Code compatibility\n\n@AGENTS.md\n@system/OPERATING_RULES.md\n"
	if string(claudeData) != wantClaude {
		t.Errorf("generated CLAUDE.md = %q, want %q", claudeData, wantClaude)
	}
	if _, err := config.Load(filepath.Join(root, "lore.yaml")); err != nil {
		t.Fatalf("load generated config: %v", err)
	}
	loreInfo, err := os.Stat(filepath.Join(root, ".lore"))
	if err != nil {
		t.Fatal(err)
	}
	if loreInfo.Mode().Perm() != 0o700 {
		t.Fatalf(".lore mode = %o, want 700", loreInfo.Mode().Perm())
	}
	if !first.Validation.Valid {
		t.Fatalf("first init validation: %+v", first.Validation)
	}

	second, err := Initialize(context.Background(), options, gitx.New())
	if err != nil {
		t.Fatalf("Initialize second: %v", err)
	}
	if len(second.CreatedFiles) != 0 {
		t.Fatalf("second init created files: %v", second.CreatedFiles)
	}
	if !reflect.DeepEqual(second.ExistingFiles, first.CreatedFiles) {
		t.Fatalf("second existing files = %v, want %v", second.ExistingFiles, first.CreatedFiles)
	}
}

func TestInitializeGitRepositoryWithInitialCommit(t *testing.T) {
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
	result, err := Initialize(context.Background(), Options{Path: root}, gitx.New())
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if !result.GitInitialized || result.InitialCommit == "" {
		t.Fatalf("unexpected Git result: %+v", result)
	}
	command := exec.Command("git", "-C", root, "show", "--format=%s", "--no-patch", "HEAD")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git show: %v", err)
	}
	if string(output) != "init: create Lore knowledge repository\n" {
		t.Fatalf("commit subject = %q", output)
	}
}
