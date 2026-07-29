package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
)

func (m *Manager) Status(ctx context.Context, full bool) (status Status, returnErr error) {
	status = emptyStatus(full)
	if m == nil || m.Repo == nil || m.Git == nil {
		return status, newError(ErrorRuntime, "index_unavailable", "index manager is not fully configured", nil)
	}
	indexPaths, err := resolvePaths(m.Repo, false)
	if err != nil {
		status.IndexState = StateCorrupt
		status.Warnings = append(status.Warnings, Warning{Code: "unsafe_index_path", Message: "the index path is unsafe or malformed"})
		return status, nil
	}
	operationLock, err := acquireIndexLock(indexPaths.directory, false)
	if err != nil {
		var indexErr *Error
		if errors.As(err, &indexErr) && indexErr.Code == "index_busy" {
			status.IndexState = StateBuilding
			status.Warnings = append(status.Warnings, Warning{Code: "index_operation_active", Message: "an exclusive index operation is active"})
			return status, nil
		}
		return status, classifyRuntime("index_lock_failed", "could not acquire the index operation lock", err)
	}
	defer func() {
		if releaseErr := operationLock.release(); releaseErr != nil && returnErr == nil {
			returnErr = newError(ErrorRuntime, "index_lock_release_failed", "could not release the index operation lock", releaseErr)
		}
	}()

	if _, err := os.Lstat(indexPaths.live); errors.Is(err, fs.ErrNotExist) {
		builds, buildErr := buildFiles(indexPaths.directory)
		if buildErr != nil {
			return status, newError(ErrorRuntime, "index_status_failed", "could not inspect temporary index builds", buildErr)
		}
		if len(builds) > 0 {
			status.IndexState = StateBuilding
			status.Warnings = append(status.Warnings, Warning{Code: "unfinished_index_build", Message: "one or more temporary index builds remain"})
		}
		return status, nil
	} else if err != nil {
		return status, newError(ErrorRuntime, "index_status_failed", "could not inspect the index", err)
	}

	mode := openReadOnly
	if full {
		mode = openReadWrite
	}
	db, sqliteVersion, err := openDatabase(ctx, indexPaths.live, mode, "")
	if err != nil {
		status.IndexState = StateCorrupt
		status.Warnings = append(status.Warnings, Warning{Code: "index_open_failed", Message: "the index could not be opened"})
		return status, nil
	}
	defer db.Close()
	status.SQLiteVersion = sqliteVersion

	metadata, err := readMetadata(ctx, db)
	if err != nil {
		status.IndexState = StateCorrupt
		status.Warnings = append(status.Warnings, Warning{Code: "index_metadata_invalid", Message: "required index metadata is missing or malformed"})
		return status, nil
	}
	schemaVersion, err := metadataSchemaVersion(metadata)
	if err != nil {
		status.IndexState = StateCorrupt
		status.Warnings = append(status.Warnings, Warning{Code: "index_metadata_invalid", Message: "the index schema version is malformed"})
		return status, nil
	}
	status.IndexSchemaVersion = schemaVersion
	status.IndexedHead = metadata["indexed_head"]
	status.IndexedBranch = metadata["indexed_branch"]
	status.IndexedAt = metadata["indexed_at"]
	status.IndexBuildID = metadata["index_build_id"]
	if schemaVersion != SchemaVersion {
		status.IndexState = StateIncompatible
		status.Warnings = append(status.Warnings, Warning{Code: "index_schema_incompatible", Message: "the index schema is incompatible and must be rebuilt"})
		return status, nil
	}
	if metadata["build_complete"] != "true" {
		status.IndexState = StateBuilding
		status.Warnings = append(status.Warnings, Warning{Code: "index_build_incomplete", Message: "the installed index is not marked as a complete build"})
		return status, nil
	}

	snapshot, snapshotErr := m.currentSnapshot(ctx, false)
	if snapshotErr != nil {
		status.IndexState = StateIncompatible
		status.Warnings = append(status.Warnings, Warning{Code: "repository_identity_unavailable", Message: "the current repository identity cannot be verified"})
		return status, nil
	}
	status.CurrentHead = snapshot.head
	status.CurrentBranch = snapshot.branch
	status.RepositoryIdentityMatches = metadata["repository_identity"] == snapshot.identity
	if !status.RepositoryIdentityMatches {
		status.IndexState = StateIncompatible
		status.Warnings = append(status.Warnings, Warning{Code: "repository_identity_mismatch", Message: "the index belongs to a different repository"})
		return status, nil
	}

	verification, err := verifyDatabase(ctx, db, full, true, snapshot.identity, -1)
	if err != nil {
		status.IndexState = StateCorrupt
		status.Warnings = append(status.Warnings, Warning{Code: "index_verification_failed", Message: "the index failed consistency verification"})
		return status, nil
	}
	status.DocumentCount = verification.documentCount
	status.PageCount = verification.pageCount
	status.SourceCount = verification.sourceCount
	status.SecureDelete = verification.secureDelete
	status.FTS5SecureDelete = verification.ftsSecureDelete
	if full && !status.FTS5SecureDelete {
		status.Warnings = append(status.Warnings, Warning{Code: "fts5_secure_delete_disabled", Message: "FTS5 secure-delete is not enabled"})
	}
	if full && !status.SecureDelete {
		status.Warnings = append(status.Warnings, Warning{Code: "secure_delete_disabled", Message: "SQLite secure_delete is not enabled"})
	}

	if !snapshot.isGit || snapshot.head == "" {
		status.IndexState = StateUncertified
		if full {
			matches, manifestErr := m.manifestMatches(ctx, db)
			if manifestErr != nil {
				status.Warnings = append(status.Warnings, Warning{Code: "manifest_verification_failed", Message: "the canonical file manifest could not be compared"})
			} else {
				status.ManifestMatches = matches
				if !matches {
					status.Warnings = append(status.Warnings, Warning{Code: "manifest_mismatch", Message: "the canonical file manifest differs from the index"})
				}
			}
		}
		status.Warnings = append(status.Warnings, Warning{Code: "non_git_index_uncertified", Message: "Git history cannot certify index freshness"})
		return status, nil
	}

	changes, err := m.Git.Changes(ctx, m.Repo.Root, []string{"pages", "sources"})
	if err != nil {
		return status, newError(ErrorRuntime, "git_status_failed", "could not inspect managed Git paths", err)
	}
	status.ManagedWorktreeClean = len(changes) == 0
	recoveryActive, err := recoveryExists(m.Repo)
	if err != nil {
		return status, newError(ErrorRuntime, "recovery_check_failed", "could not inspect transaction recovery state", err)
	}
	if status.IndexedHead != snapshot.head ||
		status.IndexedBranch != snapshot.branch ||
		!status.ManagedWorktreeClean ||
		recoveryActive {
		status.IndexState = StateStale
		if status.IndexedHead != snapshot.head {
			status.Warnings = append(status.Warnings, Warning{Code: "indexed_head_changed", Message: "Git HEAD differs from the indexed snapshot"})
		}
		if status.IndexedBranch != snapshot.branch {
			status.Warnings = append(status.Warnings, Warning{Code: "indexed_branch_changed", Message: "the current branch differs from the indexed snapshot"})
		}
		if !status.ManagedWorktreeClean {
			status.Warnings = append(status.Warnings, Warning{Code: "managed_worktree_dirty", Message: "pages or sources contain tracked or untracked changes"})
		}
		if recoveryActive {
			status.Warnings = append(status.Warnings, Warning{Code: "recovery_active", Message: "an active transaction recovery journal makes the index stale"})
		}
		return status, nil
	}
	status.IndexState = StateFresh
	return status, nil
}

func emptyStatus(full bool) Status {
	verification := "lightweight"
	if full {
		verification = "full"
	}
	return Status{
		SchemaVersion: SchemaVersion,
		Status:        "ok",
		IndexState:    StateMissing,
		Path:          RelativeIndexPath,
		Verification:  verification,
		Warnings:      []Warning{},
	}
}

func recoveryExists(repo interface {
	SafeRepositoryPath(string) (string, error)
}) (bool, error) {
	path, err := repo.SafeRepositoryPath(".lore/recovery/active")
	if err != nil {
		return false, err
	}
	_, err = os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func (m *Manager) manifestMatches(ctx context.Context, db *sql.DB) (bool, error) {
	current, err := m.scanDocuments(ctx, "")
	if err != nil {
		return false, err
	}
	rows, err := db.QueryContext(ctx, "SELECT path, revision FROM documents ORDER BY path")
	if err != nil {
		return false, fmt.Errorf("read indexed manifest: %w", err)
	}
	defer rows.Close()
	indexed := map[string]string{}
	for rows.Next() {
		var path, revision string
		if err := rows.Scan(&path, &revision); err != nil {
			return false, fmt.Errorf("decode indexed manifest: %w", err)
		}
		indexed[path] = revision
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("read indexed manifest: %w", err)
	}
	if len(indexed) != len(current) {
		return false, nil
	}
	sort.Slice(current, func(i, j int) bool { return current[i].Path < current[j].Path })
	for _, document := range current {
		if indexed[document.Path] != document.Revision {
			return false, nil
		}
	}
	return true, nil
}
