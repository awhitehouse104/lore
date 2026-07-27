package repository

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func testRepository(t *testing.T) *Repository {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"pages", "sources"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &Repository{Root: root}
}

func TestSafeContentPath(t *testing.T) {
	repo := testRepository(t)
	path, err := repo.SafeContentPath("pages/foo.md")
	if err != nil {
		t.Fatalf("SafeContentPath: %v", err)
	}
	if path != filepath.Join(repo.Root, "pages", "foo.md") {
		t.Fatalf("path = %q", path)
	}
}

func TestSafeContentPathRejectsUnsafePaths(t *testing.T) {
	repo := testRepository(t)
	tests := []string{
		"",
		"/etc/passwd",
		"../outside",
		"pages/../../outside",
		"assets/file.md",
		"pages/\x00bad",
	}
	for _, path := range tests {
		if _, err := repo.SafeContentPath(path); err == nil {
			t.Fatalf("SafeContentPath(%q) unexpectedly succeeded", path)
		}
	}
}

func TestSafeContentPathRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics and privileges differ on Windows")
	}
	repo := testRepository(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo.Root, "pages", "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SafeContentPath("pages/escape/secret.md"); err == nil {
		t.Fatal("SafeContentPath unexpectedly allowed a symlink escape")
	}
}

func TestResolvePrecedenceAndWalkUp(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	for _, root := range []string{first, second} {
		if err := os.WriteFile(filepath.Join(root, "lore.yaml"), []byte("version: 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	nested := filepath.Join(second, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	getenv := func(key string) string {
		if key == "LORE_REPO" {
			return second
		}
		return ""
	}

	got, err := Resolve(first, nested, getenv)
	if err != nil || got != first {
		t.Fatalf("explicit resolve = %q, %v; want %q", got, err, first)
	}
	got, err = Resolve("", nested, getenv)
	if err != nil || got != second {
		t.Fatalf("env resolve = %q, %v; want %q", got, err, second)
	}
	got, err = Resolve("", nested, func(string) string { return "" })
	if err != nil || got != second {
		t.Fatalf("walk resolve = %q, %v; want %q", got, err, second)
	}
}
