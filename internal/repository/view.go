package repository

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// View is the read-only repository surface used by operations that must inspect
// either the real tree or a prospective tree.
type View interface {
	ManagedMarkdown() (paths []string, issues []WalkIssue, err error)
	ReadFile(relative string) ([]byte, error)
	Stat(relative string) (fs.FileInfo, error)
}

// FilesystemView reads the repository's current working tree.
type FilesystemView struct {
	Repository *Repository
}

func (v FilesystemView) ManagedMarkdown() ([]string, []WalkIssue, error) {
	return v.Repository.ManagedMarkdown()
}

func (v FilesystemView) ReadFile(relative string) ([]byte, error) {
	absolute, err := v.Repository.SafeRepositoryPath(relative)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(absolute)
}

func (v FilesystemView) Stat(relative string) (fs.FileInfo, error) {
	absolute, err := v.Repository.SafeRepositoryPath(relative)
	if err != nil {
		return nil, err
	}
	return os.Stat(absolute)
}

// OverlayView exposes exact replacement, creation, or deletion over a base
// view without mutating the authoritative filesystem.
type OverlayView struct {
	base    View
	files   map[string][]byte
	deleted map[string]struct{}
}

func NewOverlayView(repo *Repository, base View, files map[string][]byte) (*OverlayView, error) {
	return NewOverlayViewWithDeletions(repo, base, files, nil)
}

func NewOverlayViewWithDeletions(repo *Repository, base View, files map[string][]byte, deleted []string) (*OverlayView, error) {
	if base == nil {
		base = FilesystemView{Repository: repo}
	}
	copied := make(map[string][]byte, len(files))
	for path, data := range files {
		if _, err := repo.SafeContentPath(path); err != nil {
			return nil, fmt.Errorf("overlay path %s: %w", path, err)
		}
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
		copied[clean] = append([]byte(nil), data...)
	}
	removed := make(map[string]struct{}, len(deleted))
	for _, path := range deleted {
		if _, err := repo.SafeContentPath(path); err != nil {
			return nil, fmt.Errorf("deleted overlay path %s: %w", path, err)
		}
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
		if _, exists := copied[clean]; exists {
			return nil, fmt.Errorf("overlay path %s cannot have both content and deletion", path)
		}
		removed[clean] = struct{}{}
	}
	return &OverlayView{base: base, files: copied, deleted: removed}, nil
}

func (v *OverlayView) ManagedMarkdown() ([]string, []WalkIssue, error) {
	paths, issues, err := v.base.ManagedMarkdown()
	if err != nil {
		return nil, nil, err
	}
	set := make(map[string]struct{}, len(paths)+len(v.files))
	for _, path := range paths {
		if _, deleted := v.deleted[path]; deleted {
			continue
		}
		set[path] = struct{}{}
	}
	for path := range v.files {
		if filepath.Ext(path) == ".md" {
			set[path] = struct{}{}
		}
	}
	paths = paths[:0]
	for path := range set {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, issues, nil
}

func (v *OverlayView) ReadFile(relative string) ([]byte, error) {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))
	if _, deleted := v.deleted[clean]; deleted {
		return nil, &fs.PathError{Op: "read", Path: clean, Err: fs.ErrNotExist}
	}
	if data, exists := v.files[clean]; exists {
		return append([]byte(nil), data...), nil
	}
	return v.base.ReadFile(clean)
}

func (v *OverlayView) Stat(relative string) (fs.FileInfo, error) {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))
	if _, deleted := v.deleted[clean]; deleted {
		return nil, &fs.PathError{Op: "stat", Path: clean, Err: fs.ErrNotExist}
	}
	if data, exists := v.files[clean]; exists {
		return overlayFileInfo{name: filepath.Base(clean), size: int64(len(data))}, nil
	}
	return v.base.Stat(clean)
}

type overlayFileInfo struct {
	name string
	size int64
}

func (i overlayFileInfo) Name() string       { return i.name }
func (i overlayFileInfo) Size() int64        { return i.size }
func (i overlayFileInfo) Mode() fs.FileMode  { return 0o644 }
func (i overlayFileInfo) ModTime() time.Time { return time.Time{} }
func (i overlayFileInfo) IsDir() bool        { return false }
func (i overlayFileInfo) Sys() any           { return nil }
