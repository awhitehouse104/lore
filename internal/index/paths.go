package index

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"lore/internal/repository"
)

type paths struct {
	directory string
	live      string
}

func resolvePaths(repo *repository.Repository, create bool) (paths, error) {
	if repo == nil {
		return paths{}, fmt.Errorf("repository is not configured")
	}
	directory, err := repo.SafeRepositoryPath(".lore")
	if err != nil {
		return paths{}, fmt.Errorf("resolve index directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if errors.Is(err, fs.ErrNotExist) && create {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return paths{}, fmt.Errorf("create index directory: %w", err)
		}
		info, err = os.Lstat(directory)
	}
	if err != nil {
		return paths{}, fmt.Errorf("inspect index directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return paths{}, fmt.Errorf(".lore must be a real directory")
	}
	if create {
		if err := os.Chmod(directory, 0o700); err != nil {
			return paths{}, fmt.Errorf("restrict index directory permissions: %w", err)
		}
	}
	live, err := repo.SafeRepositoryPath(RelativeIndexPath)
	if err != nil {
		return paths{}, fmt.Errorf("resolve index path: %w", err)
	}
	if info, err := os.Lstat(live); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return paths{}, fmt.Errorf("index path must be a regular non-symlink file")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return paths{}, fmt.Errorf("inspect index path: %w", err)
	}
	return paths{directory: directory, live: live}, nil
}

func buildFiles(directory string) ([]string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect index directory: %w", err)
	}
	var paths []string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "index.build.") || !strings.HasSuffix(entry.Name(), ".sqlite") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect temporary index: %w", err)
		}
		if info.Mode().IsRegular() {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func syncDirectory(path string) {
	directory, err := os.Open(path)
	if err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
}
