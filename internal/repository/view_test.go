package repository_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"lore/internal/gitx"
	"lore/internal/initrepo"
	"lore/internal/repository"
)

func TestOverlayViewReadsAndListsProspectiveFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge")
	if _, err := initrepo.Initialize(context.Background(), initrepo.Options{Path: root, NoGit: true}, gitx.New()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	existingPath := filepath.Join(root, "pages", "existing.md")
	if err := os.WriteFile(existingPath, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	view, err := repository.NewOverlayView(repo, nil, map[string][]byte{
		"pages/existing.md": []byte("new"),
		"pages/created.md":  []byte("created"),
	})
	if err != nil {
		t.Fatalf("NewOverlayView: %v", err)
	}

	data, err := view.ReadFile("pages/existing.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("overlaid data = %q", data)
	}
	onDisk, err := os.ReadFile(existingPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != "old" {
		t.Fatalf("working tree changed to %q", onDisk)
	}
	info, err := view.Stat("pages/created.md")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len("created")) || !info.Mode().IsRegular() {
		t.Fatalf("created info = %+v", info)
	}
	paths, _, err := view.ManagedMarkdown()
	if err != nil {
		t.Fatal(err)
	}
	if !contains(paths, "pages/existing.md") || !contains(paths, "pages/created.md") {
		t.Fatalf("managed paths = %v", paths)
	}
}

func TestOverlayViewRejectsUnsafePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge")
	if _, err := initrepo.Initialize(context.Background(), initrepo.Options{Path: root, NoGit: true}, gitx.New()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.NewOverlayView(repo, nil, map[string][]byte{"../escape.md": []byte("x")}); err == nil {
		t.Fatal("unsafe overlay path accepted")
	}
}

func TestOverlayViewHidesDeletedFilesWithoutTouchingDisk(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge")
	if _, err := initrepo.Initialize(context.Background(), initrepo.Options{Path: root, NoGit: true}, gitx.New()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	path := filepath.Join(root, "pages", "deleted.md")
	if err := os.WriteFile(path, []byte("historical"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	view, err := repository.NewOverlayViewWithDeletions(repo, nil, nil, []string{"pages/deleted.md"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := view.ReadFile("pages/deleted.md"); !os.IsNotExist(err) {
		t.Fatalf("deleted overlay read error = %v", err)
	}
	if _, err := view.Stat("pages/deleted.md"); !os.IsNotExist(err) {
		t.Fatalf("deleted overlay stat error = %v", err)
	}
	paths, _, err := view.ManagedMarkdown()
	if err != nil {
		t.Fatal(err)
	}
	if contains(paths, "pages/deleted.md") {
		t.Fatalf("deleted path remains in prospective view: %v", paths)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "historical" {
		t.Fatalf("working tree deletion leaked from overlay: %q %v", data, err)
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
