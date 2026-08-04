package repository_test

import (
	"os"
	"path/filepath"
	"testing"

	"lore/internal/config"
	"lore/internal/repository"
)

func TestAtomicApplyCreateReplaceAndConflict(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "pages"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := &repository.Repository{Root: root, Config: config.Defaults()}
	if err := repo.AtomicApply("pages/new.md", []byte("created"), nil, false); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := repo.AtomicApply("pages/new.md", []byte("replacement"), []byte("wrong"), true); err == nil {
		t.Fatal("replacement with stale expected bytes succeeded")
	}
	if got, err := os.ReadFile(filepath.Join(root, "pages", "new.md")); err != nil || string(got) != "created" {
		t.Fatalf("file after conflict = %q, %v", got, err)
	}
	if err := repo.AtomicApply("pages/new.md", []byte("replacement"), []byte("created"), true); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "pages", "new.md")); err != nil || string(got) != "replacement" {
		t.Fatalf("file after replacement = %q, %v", got, err)
	}
	if err := repo.AtomicApply("pages/new.md", []byte("other"), nil, false); err == nil {
		t.Fatal("create overwrote existing path")
	}
	if err := repo.RemoveExpected("pages/new.md", []byte("wrong")); err == nil {
		t.Fatal("removal with stale expected bytes succeeded")
	}
	if got, err := os.ReadFile(filepath.Join(root, "pages", "new.md")); err != nil || string(got) != "replacement" {
		t.Fatalf("file after removal conflict = %q, %v", got, err)
	}
	if err := repo.RemoveExpected("pages/new.md", []byte("replacement")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "pages", "new.md")); !os.IsNotExist(err) {
		t.Fatalf("removed file still exists: %v", err)
	}
	if err := os.Symlink("missing.md", filepath.Join(root, "pages", "linked.md")); err != nil {
		t.Fatal(err)
	}
	if err := repo.RemoveExpected("pages/linked.md", nil); err == nil {
		t.Fatal("symlink removal succeeded")
	}
}
