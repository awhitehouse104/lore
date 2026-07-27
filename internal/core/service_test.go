package core_test

import (
	"context"
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
	"lore/internal/lock"
	"lore/internal/repository"
)

const fixedID = "src_01ARZ3NDEKTSV4RRFFQ69G5FAV"

type fixedClock struct {
	value time.Time
}

func (c fixedClock) Now() time.Time {
	return c.value
}

type fixedIDs struct {
	value string
	err   error
}

func (g fixedIDs) New(time.Time) (string, error) {
	return g.value, g.err
}

type fakeGit struct {
	commit     string
	commitErr  error
	pushErr    error
	commitPath string
	subject    string
	pushed     bool
}

func (g *fakeGit) CommitPath(_ context.Context, _, path, subject string) (string, error) {
	g.commitPath = path
	g.subject = subject
	return g.commit, g.commitErr
}

func (g *fakeGit) PushHead(_ context.Context, _, _ string) error {
	g.pushed = true
	return g.pushErr
}

func newServiceRepository(t *testing.T) *repository.Repository {
	t.Helper()
	root := filepath.Join(t.TempDir(), "knowledge")
	if _, err := initrepo.Initialize(context.Background(), initrepo.Options{Path: root, NoGit: true}, gitx.New()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return repo
}

func testService(repo *repository.Repository, git core.CaptureGit) *core.Service {
	return &core.Service{
		Repo:  repo,
		Git:   git,
		Clock: fixedClock{value: time.Date(2026, 7, 22, 16, 30, 21, 123456789, time.UTC)},
		IDs:   fixedIDs{value: fixedID},
	}
}

func TestCapturePreservesExactBytes(t *testing.T) {
	repo := newServiceRepository(t)
	git := &fakeGit{}
	service := testService(repo, git)
	body := []byte("Unicode café\r\nwithout final newline")
	result, err := service.Capture(context.Background(), core.CaptureOptions{
		Kind:        "user_statement",
		Origin:      "codex",
		Body:        body,
		NoCommit:    true,
		Sensitivity: "normal",
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if result.Path != "sources/2026/07/"+fixedID+"-user_statement.md" || !result.Written || result.Committed {
		t.Fatalf("result: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(repo.Root, filepath.FromSlash(result.Path)))
	if err != nil {
		t.Fatal(err)
	}
	document, err := docs.Parse(result.Path, data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if string(document.Body) != string(body) {
		t.Fatalf("body = %q, want %q", document.Body, body)
	}
	if document.Source.RawSHA256 != docs.SHA256(body) || result.RawSHA256 != docs.SHA256(body) {
		t.Fatalf("hash mismatch: source=%q result=%q", document.Source.RawSHA256, result.RawSHA256)
	}
}

func TestCaptureLockContentionHasDiagnosticsWithoutBody(t *testing.T) {
	repo := newServiceRepository(t)
	now := time.Date(2026, 7, 22, 16, 30, 21, 0, time.UTC)
	handle, err := lock.Acquire(repo.Root, "other-writer", now)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Release()
	service := testService(repo, &fakeGit{})
	secret := "do-not-leak-this-body"
	_, err = service.Capture(context.Background(), core.CaptureOptions{
		Kind: "user_statement", Origin: "codex", Body: []byte(secret),
	})
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.ExitCode != core.ExitConflict || apiErr.Code != "repository_locked" {
		t.Fatalf("Capture error = %T %v", err, err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked source body: %v", err)
	}
	if apiErr.Details["pid"] == nil || apiErr.Details["lock_path"] == nil {
		t.Fatalf("lock details: %+v", apiErr.Details)
	}
}

func TestCaptureCommitFailurePreservesSource(t *testing.T) {
	repo := newServiceRepository(t)
	secret := "preserved but never logged"
	git := &fakeGit{commitErr: errors.New("author identity unavailable")}
	service := testService(repo, git)
	result, err := service.Capture(context.Background(), core.CaptureOptions{
		Kind: "user_statement", Origin: "codex", Body: []byte(secret),
	})
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "git_commit_failed" {
		t.Fatalf("Capture error = %T %v", err, err)
	}
	if !result.Written || result.Path == "" {
		t.Fatalf("partial result: %+v", result)
	}
	if _, statErr := os.Stat(filepath.Join(repo.Root, filepath.FromSlash(result.Path))); statErr != nil {
		t.Fatalf("captured source missing after commit failure: %v", statErr)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked source body: %v", err)
	}
}

func TestCapturePushFailurePolicy(t *testing.T) {
	for _, requirePush := range []bool{false, true} {
		t.Run(map[bool]string{false: "warning", true: "required"}[requirePush], func(t *testing.T) {
			repo := newServiceRepository(t)
			repo.Config.Git.RequirePush = requirePush
			git := &fakeGit{commit: strings.Repeat("a", 40), pushErr: errors.New("remote unavailable")}
			service := testService(repo, git)
			push := true
			result, err := service.Capture(context.Background(), core.CaptureOptions{
				Kind: "user_statement", Origin: "codex", Body: []byte("hello"), Push: &push,
			})
			if requirePush {
				var apiErr *core.APIError
				if !errors.As(err, &apiErr) || apiErr.Code != "git_push_failed" || !result.Committed {
					t.Fatalf("required push result=%+v err=%v", result, err)
				}
			} else {
				if err != nil || !result.Committed || result.Pushed || len(result.Warnings) != 1 {
					t.Fatalf("optional push result=%+v err=%v", result, err)
				}
			}
		})
	}
}

func TestCaptureCommitsOnlySourceAndPreservesDirtyState(t *testing.T) {
	requireGit(t)
	root := initializeGitRepository(t)
	pagePath := filepath.Join(root, "pages", "project-foo.md")
	page := `---
id: page_project_foo
title: Project Foo
kind: project
created: "2026-07-22"
updated: "2026-07-22"
status: active
sensitivity: normal
---
# Summary
`
	if err := os.WriteFile(pagePath, []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "--", "pages/project-foo.md")
	runGit(t, root, "commit", "-m", "test: add page")

	if err := os.WriteFile(pagePath, []byte(page+"\nDirty page edit.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readme := filepath.Join(root, "README.md")
	original, err := os.ReadFile(readme)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(readme, append(original, []byte("\nStaged unrelated edit.\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "--", "README.md")

	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	service := testService(repo, gitx.New())
	result, err := service.Capture(context.Background(), core.CaptureOptions{
		Kind: "user_statement", Origin: "codex", Body: []byte("exact source"),
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	committed := strings.Fields(runGit(t, root, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"))
	if len(committed) != 1 || committed[0] != result.Path {
		t.Fatalf("capture commit paths = %v, want [%s]", committed, result.Path)
	}
	unstaged := strings.Fields(runGit(t, root, "diff", "--name-only"))
	if len(unstaged) != 1 || unstaged[0] != "pages/project-foo.md" {
		t.Fatalf("unstaged paths after capture = %v", unstaged)
	}
	staged := strings.Fields(runGit(t, root, "diff", "--cached", "--name-only"))
	if len(staged) != 1 || staged[0] != "README.md" {
		t.Fatalf("staged paths after capture = %v", staged)
	}
}

func TestCaptureRealCommitFailurePreservesValidSource(t *testing.T) {
	requireGit(t)
	root := initializeGitRepository(t)
	emptyConfig := filepath.Join(t.TempDir(), "empty-gitconfig")
	if err := os.WriteFile(emptyConfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", emptyConfig)

	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	service := testService(repo, gitx.New())
	body := []byte("this source survives commit failure")
	result, err := service.Capture(context.Background(), core.CaptureOptions{
		Kind: "user_statement", Origin: "codex", Body: body,
	})
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "git_commit_failed" {
		t.Fatalf("Capture result=%+v error=%T %v", result, err, err)
	}
	data, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(result.Path)))
	if readErr != nil {
		t.Fatalf("source missing: %v", readErr)
	}
	document, parseErr := docs.Parse(result.Path, data)
	if parseErr != nil {
		t.Fatalf("source invalid: %v", parseErr)
	}
	if string(document.Body) != string(body) || docs.SHA256(document.Body) != document.Source.RawSHA256 {
		t.Fatalf("source was not preserved exactly")
	}
}

func TestCapturePushesToLocalBareRemote(t *testing.T) {
	requireGit(t)
	root := initializeGitRepository(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	command := exec.Command("git", "init", "--bare", remote)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, output)
	}
	runGit(t, root, "remote", "add", "origin", remote)
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	service := testService(repo, gitx.New())
	push := true
	result, err := service.Capture(context.Background(), core.CaptureOptions{
		Kind: "user_statement", Origin: "codex", Body: []byte("push me"), Push: &push,
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if !result.Pushed || !result.Committed {
		t.Fatalf("result: %+v", result)
	}
	command = exec.Command("git", "--git-dir", remote, "rev-parse", "refs/heads/main")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("remote rev-parse: %v", err)
	}
	if strings.TrimSpace(string(output)) != result.Commit {
		t.Fatalf("remote head = %q, want %q", output, result.Commit)
	}
}

func TestCaptureRealPushFailureKeepsLocalCommit(t *testing.T) {
	requireGit(t)
	root := initializeGitRepository(t)
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	service := testService(repo, gitx.New())
	push := true
	result, err := service.Capture(context.Background(), core.CaptureOptions{
		Kind: "user_statement", Origin: "codex", Body: []byte("local durability"), Push: &push,
	})
	if err != nil {
		t.Fatalf("optional push failure returned error: %v", err)
	}
	if !result.Committed || result.Pushed || len(result.Warnings) == 0 {
		t.Fatalf("result: %+v", result)
	}
	if head := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD")); head != result.Commit {
		t.Fatalf("local HEAD = %q, want %q", head, result.Commit)
	}
	if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(result.Path))); statErr != nil {
		t.Fatalf("source missing after push failure: %v", statErr)
	}
}

func TestReadResolutionAndAmbiguity(t *testing.T) {
	repo := newServiceRepository(t)
	firstPath := filepath.Join(repo.Root, "pages", "project-foo.md")
	first := []byte(`---
id: page_project_foo
title: Project Foo
kind: project
aliases:
  - foo
  - shared
created: "2026-07-22"
updated: "2026-07-22"
status: active
sensitivity: normal
---
# Summary

Line two.
`)
	secondPath := filepath.Join(repo.Root, "pages", "other.md")
	second := []byte(`---
id: page_other
title: Other
kind: topic
aliases:
  - shared
created: "2026-07-22"
updated: "2026-07-22"
status: active
sensitivity: normal
---
Other body.
`)
	if err := os.WriteFile(firstPath, first, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, second, 0o644); err != nil {
		t.Fatal(err)
	}
	service := core.NewService(repo)
	references := []string{
		"pages/project-foo.md",
		"page_project_foo",
		"project-foo",
		"PROJECT FOO",
		"FOO",
	}
	for _, reference := range references {
		result, err := service.Read(context.Background(), reference, nil)
		if err != nil {
			t.Errorf("Read(%q): %v", reference, err)
			continue
		}
		if result.Path != "pages/project-foo.md" || result.Content != string(first) {
			t.Errorf("Read(%q) = %+v", reference, result)
		}
	}
	requested := &core.LineRange{Start: 13, End: 200}
	result, err := service.Read(context.Background(), "page_project_foo", requested)
	if err != nil {
		t.Fatalf("Read range: %v", err)
	}
	if result.LineStart != 13 || result.LineEnd != 15 || result.Content != "# Summary\n\nLine two.\n" {
		t.Fatalf("range result: %+v", result)
	}
	_, err = service.Read(context.Background(), "shared", nil)
	var ambiguous *core.APIError
	if !errors.As(err, &ambiguous) || ambiguous.Code != "ambiguous_reference" || ambiguous.ExitCode != core.ExitConflict {
		t.Fatalf("ambiguous read error = %T %v", err, err)
	}
	candidates, ok := ambiguous.Details["candidates"].([]string)
	if !ok || len(candidates) != 2 || candidates[0] != "pages/other.md" || candidates[1] != "pages/project-foo.md" {
		t.Fatalf("ambiguity candidates = %#v", ambiguous.Details["candidates"])
	}
	_, err = service.Read(context.Background(), "../outside", nil)
	var unsafe *core.APIError
	if !errors.As(err, &unsafe) || unsafe.Code != "unsafe_reference" {
		t.Fatalf("unsafe read error = %T %v", err, err)
	}
}

func TestRecentHistoryContentFilterAndAll(t *testing.T) {
	requireGit(t)
	root := initializeGitRepository(t)
	readmePath := filepath.Join(root, "README.md")
	readme, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(readmePath, append(readme, []byte("\nConfig-only history.\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "--", "README.md")
	runGit(t, root, "commit", "-m", "test: config only")

	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	captureService := testService(repo, gitx.New())
	captured, err := captureService.Capture(context.Background(), core.CaptureOptions{
		Kind: "user_statement", Origin: "codex", Body: []byte("history evidence"),
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}

	service := core.NewService(repo)
	content, err := service.Recent(context.Background(), core.RecentOptions{Limit: 20})
	if err != nil {
		t.Fatalf("Recent content: %v", err)
	}
	for _, commit := range content.Commits {
		if commit.Subject == "test: config only" {
			t.Fatalf("content history included config-only commit: %+v", content.Commits)
		}
		if len(commit.Hash) != 40 || commit.CommittedAt.IsZero() {
			t.Fatalf("invalid commit fields: %+v", commit)
		}
	}
	if len(content.Commits) == 0 || content.Commits[0].Hash != captured.Commit {
		t.Fatalf("content history does not begin with capture: %+v", content.Commits)
	}

	all, err := service.Recent(context.Background(), core.RecentOptions{Limit: 20, All: true})
	if err != nil {
		t.Fatalf("Recent all: %v", err)
	}
	foundConfig := false
	for _, commit := range all.Commits {
		if commit.Subject == "test: config only" {
			foundConfig = true
		}
	}
	if !foundConfig {
		t.Fatalf("all history excluded config-only commit: %+v", all.Commits)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
}

func initializeGitRepository(t *testing.T) string {
	t.Helper()
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
	return root
}

func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", root}, args...)
	command := exec.Command("git", commandArgs...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
