package initrepo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"lore/internal/core"
	"lore/internal/gitx"
	"lore/internal/lint"
	"lore/internal/repository"
	"lore/templates"
)

type Options struct {
	Path  string
	NoGit bool
}

type Result struct {
	SchemaVersion  int         `json:"schema_version"`
	Repo           string      `json:"repo"`
	CreatedFiles   []string    `json:"created_files"`
	ExistingFiles  []string    `json:"existing_files"`
	GitInitialized bool        `json:"git_initialized"`
	InitialCommit  string      `json:"initial_commit,omitempty"`
	Warnings       []string    `json:"warnings"`
	Validation     lint.Result `json:"-"`
}

func Initialize(ctx context.Context, options Options, git gitx.Client) (Result, error) {
	path := options.Path
	if path == "" {
		var err error
		path, err = os.Getwd()
		if err != nil {
			return Result{}, fmt.Errorf("get current directory: %w", err)
		}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Result{}, fmt.Errorf("resolve initialization path: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o755); err != nil {
		return Result{}, fmt.Errorf("create repository root: %w", err)
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return Result{}, fmt.Errorf("resolve repository root: %w", err)
	}

	result := Result{
		SchemaVersion: core.SchemaVersion,
		Repo:          absolute,
		CreatedFiles:  []string{},
		ExistingFiles: []string{},
		Warnings:      []string{},
	}
	for _, dir := range []string{"pages", "sources", "assets", "system", ".lore"} {
		if err := ensureDirectory(filepath.Join(absolute, dir)); err != nil {
			return Result{}, err
		}
	}

	templateErr := fs.WalkDir(templates.Knowledge, "knowledge", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative := strings.TrimPrefix(filepath.ToSlash(path), "knowledge/")
		data, readErr := templates.Knowledge.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		created, createErr := createFile(filepath.Join(absolute, filepath.FromSlash(relative)), data)
		if createErr != nil {
			return createErr
		}
		if created {
			result.CreatedFiles = append(result.CreatedFiles, relative)
		} else {
			result.ExistingFiles = append(result.ExistingFiles, relative)
		}
		return nil
	})
	if templateErr != nil {
		return Result{}, fmt.Errorf("install initialization templates: %w", templateErr)
	}
	for _, relative := range []string{"pages/.gitkeep", "sources/.gitkeep", "assets/.gitkeep"} {
		created, createErr := createFile(filepath.Join(absolute, filepath.FromSlash(relative)), nil)
		if createErr != nil {
			return Result{}, createErr
		}
		if created {
			result.CreatedFiles = append(result.CreatedFiles, relative)
		} else {
			result.ExistingFiles = append(result.ExistingFiles, relative)
		}
	}
	sort.Strings(result.CreatedFiles)
	sort.Strings(result.ExistingFiles)

	isGit := false
	if !options.NoGit {
		isGit, err = git.IsRepository(ctx, absolute)
		if err != nil {
			return Result{}, err
		}
		if !isGit {
			if err := git.Init(ctx, absolute, "main"); err != nil {
				return Result{}, err
			}
			result.GitInitialized = true
			isGit = true
		}
	}

	repo, err := repository.Open(absolute)
	if err != nil {
		return Result{}, err
	}
	validation, err := lint.Run(ctx, repo, git)
	if err != nil {
		return Result{}, err
	}
	result.Validation = validation
	if !validation.Valid {
		return result, &core.APIError{
			Code:     "repository_invalid",
			Message:  fmt.Sprintf("initialized files failed validation with %d error(s)", validation.Errors),
			Details:  map[string]any{"findings": validation.Findings},
			ExitCode: core.ExitValidation,
		}
	}

	if result.GitInitialized && isGit {
		identity, identityErr := git.IdentityConfigured(ctx, absolute)
		if identityErr != nil {
			return Result{}, identityErr
		}
		if !identity {
			result.Warnings = append(result.Warnings,
				"Git author identity is not configured; run `git config --global user.name \"Your Name\"` and `git config --global user.email \"you@example.com\"` before enabling capture auto-commits")
		} else {
			if err := git.AddAll(ctx, absolute); err != nil {
				return Result{}, err
			}
			commit, commitErr := git.CommitAll(ctx, absolute, "init: create Lore knowledge repository")
			if commitErr != nil {
				return Result{}, commitErr
			}
			result.InitialCommit = commit
		}
	}
	return result, nil
}

func ensureDirectory(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("required directory path is not a directory: %s", path)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect directory %s: %w", path, err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", path, err)
	}
	return nil
}

func createFile(path string, data []byte) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("create parent directory for %s: %w", path, err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			info, statErr := os.Lstat(path)
			if statErr != nil {
				return false, fmt.Errorf("inspect existing file %s: %w", path, statErr)
			}
			if !info.Mode().IsRegular() {
				return false, fmt.Errorf("existing initialization path is not a regular file: %s", path)
			}
			return false, nil
		}
		return false, fmt.Errorf("create file %s: %w", path, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return false, fmt.Errorf("write file %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("close file %s: %w", path, err)
	}
	return true, nil
}
