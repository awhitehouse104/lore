package index

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (m *Manager) Clear() (result ClearResult, returnErr error) {
	result = ClearResult{SchemaVersion: SchemaVersion, Status: "ok", Removed: []string{}}
	if m == nil || m.Repo == nil {
		return result, newError(ErrorRuntime, "index_unavailable", "index manager is not fully configured", nil)
	}
	indexPaths, err := resolvePaths(m.Repo, true)
	if err != nil {
		return result, newError(ErrorRuntime, "unsafe_index_path", "could not resolve the index path", err)
	}
	operationLock, err := acquireIndexLock(indexPaths.directory, true)
	if err != nil {
		return result, classifyRuntime("index_lock_failed", "could not acquire the index operation lock", err)
	}
	defer func() {
		if releaseErr := operationLock.release(); releaseErr != nil && returnErr == nil {
			returnErr = newError(ErrorRuntime, "index_lock_release_failed", "index clear completed but its operation lock could not be released", releaseErr)
		}
	}()

	entries, err := os.ReadDir(indexPaths.directory)
	if err != nil {
		return result, newError(ErrorRuntime, "index_clear_failed", "could not inspect the index directory", err)
	}
	var targets []string
	for _, entry := range entries {
		name := entry.Name()
		if name != "index.sqlite" &&
			name != "index.sqlite-wal" &&
			name != "index.sqlite-shm" &&
			!strings.HasPrefix(name, "index.build.") {
			continue
		}
		targets = append(targets, name)
	}
	sort.Strings(targets)
	for _, name := range targets {
		path := filepath.Join(indexPaths.directory, name)
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return result, newError(ErrorRuntime, "index_clear_failed", "could not inspect a derived index file", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return result, newError(ErrorRuntime, "unsafe_index_path", "a derived index path is not a regular non-symlink file", nil)
		}
		if err := os.Remove(path); err != nil {
			return result, newError(ErrorRuntime, "index_clear_failed", "could not remove a derived index file", err)
		}
		result.Removed = append(result.Removed, ".lore/"+name)
	}
	result.Existed = len(result.Removed) > 0
	syncDirectory(indexPaths.directory)
	return result, nil
}
