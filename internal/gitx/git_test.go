package gitx_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"lore/internal/gitx"
)

func TestCommandErrorDoesNotExposeCapturedOutput(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "fake-git")
	secret := "private source body sentinel"
	script := "#!/bin/sh\nprintf '%s\\n' '" + secret + "' >&2\nexit 1\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	client := gitx.Client{Executable: executable}
	_, err := client.Head(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("fake Git command unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Git error exposed captured output: %v", err)
	}
}

func TestFetchCompareAndFastForwardUseFetchedTrackingRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(globalConfig, []byte("[user]\n\tname = Lore Test\n\temail = lore@example.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	remote := filepath.Join(t.TempDir(), "remote.git")
	gitTestCommand(t, t.TempDir(), "init", "--bare", remote)
	writer := filepath.Join(t.TempDir(), "writer")
	gitTestCommand(t, t.TempDir(), "init", "-b", "main", writer)
	if err := os.WriteFile(filepath.Join(writer, "note.md"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, writer, "add", "--", "note.md")
	gitTestCommand(t, writer, "commit", "-m", "initial")
	gitTestCommand(t, writer, "remote", "add", "origin", remote)
	gitTestCommand(t, writer, "push", "-u", "origin", "main")

	reader := filepath.Join(t.TempDir(), "reader")
	gitTestCommand(t, t.TempDir(), "clone", "--branch", "main", remote, reader)
	if err := os.WriteFile(filepath.Join(writer, "note.md"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, writer, "add", "--", "note.md")
	gitTestCommand(t, writer, "commit", "-m", "advance")
	gitTestCommand(t, writer, "push", "origin", "main")

	client := gitx.New()
	if err := client.FetchBranch(t.Context(), reader, "origin", "main"); err != nil {
		t.Fatalf("FetchBranch: %v", err)
	}
	counts, err := client.AheadBehind(t.Context(), reader, "origin", "main")
	if err != nil {
		t.Fatalf("AheadBehind: %v", err)
	}
	if counts.Ahead != 0 || counts.Behind != 1 {
		t.Fatalf("counts = %+v, want ahead 0 behind 1", counts)
	}
	if err := client.FastForward(t.Context(), reader, "origin", "main"); err != nil {
		t.Fatalf("FastForward: %v", err)
	}
	counts, err = client.AheadBehind(t.Context(), reader, "origin", "main")
	if err != nil {
		t.Fatal(err)
	}
	if counts.Ahead != 0 || counts.Behind != 0 {
		t.Fatalf("post-fast-forward counts = %+v", counts)
	}
	data, err := os.ReadFile(filepath.Join(reader, "note.md"))
	if err != nil || string(data) != "two\n" {
		t.Fatalf("reader note = %q, err = %v", data, err)
	}
}

func TestFetchBranchRejectsOptionLikeBranch(t *testing.T) {
	client := gitx.New()
	if err := client.FetchBranch(t.Context(), t.TempDir(), "origin", "--upload-pack=bad"); err == nil {
		t.Fatal("FetchBranch accepted an option-like branch")
	}
}

func gitTestCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", dir}, args...)
	command := exec.Command("git", commandArgs...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
