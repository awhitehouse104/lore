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
}
