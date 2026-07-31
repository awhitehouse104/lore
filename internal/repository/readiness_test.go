package repository

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadinessProbeChecksFixedRepositorySubstrate(t *testing.T) {
	repo := newReadyTestRepository(t)
	probe := NewReadinessProbe(repo)
	if err := probe.Check(); err != nil {
		t.Fatalf("healthy readiness = %v", err)
	}

	if err := os.WriteFile(
		filepath.Join(repo.Root, "pages", "malformed.md"),
		[]byte("not valid Lore Markdown"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join(repo.Root, "missing-target"),
		filepath.Join(repo.Root, "sources", "unmanaged-link.md"),
	); err != nil {
		t.Fatal(err)
	}
	if err := probe.Check(); err != nil {
		t.Fatalf("readiness inspected managed documents: %v", err)
	}
}

func TestReadinessProbeRejectsActiveRecoveryUntilRemoved(t *testing.T) {
	repo := newReadyTestRepository(t)
	probe := NewReadinessProbe(repo)
	active := filepath.Join(repo.Root, ".lore", "recovery", "active")
	if err := os.MkdirAll(active, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := probe.Check(); err == nil {
		t.Fatal("readiness accepted an active recovery journal")
	}
	if err := os.Remove(active); err != nil {
		t.Fatal(err)
	}
	if err := probe.Check(); err != nil {
		t.Fatalf("readiness after recovery removal = %v", err)
	}
}

func TestReadinessProbeRejectsRequiredDirectoryDegradation(t *testing.T) {
	tests := []struct {
		name   string
		damage func(*testing.T, string)
	}{
		{
			name: "missing",
			damage: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Rename(path, path+".missing"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "regular file",
			damage: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Rename(path, path+".directory"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink",
			damage: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Rename(path, path+".directory"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".directory", path); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newReadyTestRepository(t)
			probe := NewReadinessProbe(repo)
			test.damage(t, filepath.Join(repo.Root, "pages"))
			if err := probe.Check(); err == nil {
				t.Fatal("readiness accepted a degraded required directory")
			}
		})
	}
}

func TestReadinessProbeRejectsStartupConfigurationDrift(t *testing.T) {
	t.Run("in-place edit", func(t *testing.T) {
		repo := newReadyTestRepository(t)
		probe := NewReadinessProbe(repo)
		path := filepath.Join(repo.Root, "lore.yaml")
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString("# changed after startup\n"); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := probe.Check(); err == nil {
			t.Fatal("readiness accepted edited startup configuration")
		}
	})

	t.Run("atomic replacement", func(t *testing.T) {
		repo := newReadyTestRepository(t)
		probe := NewReadinessProbe(repo)
		path := filepath.Join(repo.Root, "lore.yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path, path+".original"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := probe.Check(); err == nil {
			t.Fatal("readiness accepted replaced startup configuration")
		}
	})
}

func TestReadinessProbeRejectsRepositoryRootReplacement(t *testing.T) {
	repo := newReadyTestRepository(t)
	probe := NewReadinessProbe(repo)
	original := repo.Root + ".original"
	if err := os.Rename(repo.Root, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(repo.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := probe.Check(); err == nil {
		t.Fatal("readiness accepted a replacement repository root")
	}
}

func newReadyTestRepository(t *testing.T) *Repository {
	t.Helper()
	parent := t.TempDir()
	root := filepath.Join(parent, "repository")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, relative := range requiredRepositoryDirectories {
		mode := os.FileMode(0o755)
		if relative == ".lore" {
			mode = 0o700
		}
		if err := os.Mkdir(filepath.Join(root, relative), mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "lore.yaml"), []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}
