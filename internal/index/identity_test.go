package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"lore/internal/gitx"
)

func TestNonGitRepositoryIdentityIsStableAndPrivate(t *testing.T) {
	repo := newTestRepository(t)
	manager := NewManager(repo, gitx.New(), "test")
	first, err := manager.currentSnapshot(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.currentSnapshot(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if first.identity == "" || first.identity != second.identity {
		t.Fatalf("identities differ: %q != %q", first.identity, second.identity)
	}
	info, err := os.Stat(filepath.Join(repo.Root, ".lore", "repository-id"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("repository-id mode = %o, want 600", info.Mode().Perm())
	}
}
