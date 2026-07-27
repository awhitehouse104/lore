package repository

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"lore/internal/config"
)

var contentRoots = []string{"pages", "sources"}

type Repository struct {
	Root   string
	Config config.Config
}

type WalkIssue struct {
	Path    string
	Code    string
	Message string
}

func Open(root string) (*Repository, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve repository path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve repository path: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("inspect repository path: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("repository path is not a directory: %s", resolved)
	}
	cfg, err := config.Load(filepath.Join(resolved, "lore.yaml"))
	if err != nil {
		return nil, err
	}
	return &Repository{Root: resolved, Config: cfg}, nil
}

// Resolve applies the global repository precedence rules.
func Resolve(explicit, cwd string, getenv func(string) string) (string, error) {
	if explicit != "" {
		return resolveCandidate(explicit)
	}
	if env := getenv("LORE_REPO"); env != "" {
		return resolveCandidate(env)
	}
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get current directory: %w", err)
		}
	}
	current, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve current directory: %w", err)
	}
	for {
		info, statErr := os.Stat(filepath.Join(current, "lore.yaml"))
		if statErr == nil && info.Mode().IsRegular() {
			return filepath.EvalSymlinks(current)
		}
		if statErr != nil && !os.IsNotExist(statErr) {
			return "", fmt.Errorf("inspect %s: %w", filepath.Join(current, "lore.yaml"), statErr)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return "", fmt.Errorf("no Lore repository found; use --repo PATH, set LORE_REPO, or run from beneath a directory containing lore.yaml")
}

func resolveCandidate(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve repository path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve repository path: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect repository path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repository path is not a directory: %s", resolved)
	}
	return resolved, nil
}

func (r *Repository) Relative(path string) (string, error) {
	relative, err := filepath.Rel(r.Root, path)
	if err != nil {
		return "", fmt.Errorf("make repository-relative path: %w", err)
	}
	if escapes(relative) {
		return "", fmt.Errorf("path escapes repository")
	}
	return filepath.ToSlash(relative), nil
}

// SafeContentPath resolves a repository-relative path under pages/ or sources/
// and rejects traversal, absolute paths, NUL bytes, and symlink traversal.
func (r *Repository) SafeContentPath(relative string) (string, error) {
	if strings.ContainsRune(relative, '\x00') {
		return "", fmt.Errorf("path contains a NUL byte")
	}
	if relative == "" || filepath.IsAbs(relative) {
		return "", fmt.Errorf("path must be repository-relative")
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || escapes(clean) {
		return "", fmt.Errorf("path escapes repository")
	}
	first := clean
	if index := strings.IndexRune(clean, filepath.Separator); index >= 0 {
		first = clean[:index]
	}
	if first != "pages" && first != "sources" {
		return "", fmt.Errorf("path must be under pages/ or sources/")
	}

	target := filepath.Join(r.Root, clean)
	relativeCheck, err := filepath.Rel(r.Root, target)
	if err != nil || escapes(relativeCheck) {
		return "", fmt.Errorf("path escapes repository")
	}
	if err := rejectSymlinkComponents(r.Root, clean); err != nil {
		return "", err
	}
	return target, nil
}

func (r *Repository) ManagedMarkdown() (paths []string, issues []WalkIssue, err error) {
	for _, root := range contentRoots {
		base := filepath.Join(r.Root, root)
		walkErr := filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path == base {
				return nil
			}
			rel, relErr := r.Relative(path)
			if relErr != nil {
				return relErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
					issues = append(issues, WalkIssue{
						Path:    rel,
						Code:    "managed_symlink",
						Message: "managed Markdown files must not be symlinks",
					})
				}
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() {
				if strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
					issues = append(issues, WalkIssue{
						Path:    rel,
						Code:    "managed_not_regular",
						Message: "managed Markdown files must be regular files",
					})
				}
				return nil
			}
			if strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
				paths = append(paths, rel)
			}
			return nil
		})
		if walkErr != nil {
			return nil, nil, fmt.Errorf("walk %s: %w", root, walkErr)
		}
	}
	return paths, issues, nil
}

func escapes(relative string) bool {
	return relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func rejectSymlinkComponents(root, relative string) error {
	current := root
	parts := strings.Split(relative, string(filepath.Separator))
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("inspect path component %s: %w", part, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path traverses symlink at %s", part)
		}
	}
	return nil
}
