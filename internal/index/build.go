package index

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"time"

	"github.com/oklog/ulid/v2"
)

type BuildOptions struct {
	Force bool
}

func (m *Manager) Build(ctx context.Context, options BuildOptions) (result BuildResult, returnErr error) {
	if m == nil || m.Repo == nil || m.Git == nil || m.Clock == nil {
		return result, newError(ErrorRuntime, "index_unavailable", "index manager is not fully configured", nil)
	}
	started := m.Clock.Now().UTC()
	indexPaths, err := resolvePaths(m.Repo, true)
	if err != nil {
		return result, newError(ErrorRuntime, "unsafe_index_path", "could not prepare the index path", err)
	}
	currentStatus, statusErr := m.Status(ctx, false)
	if statusErr == nil {
		switch currentStatus.IndexState {
		case StateFresh, StateUncertified:
			if !options.Force {
				return result, newError(
					ErrorConflict,
					"index_already_current",
					"a current compatible index already exists; use index update or index build --force",
					nil,
				)
			}
		case StateBuilding:
			return result, newError(ErrorConflict, "index_building", "an unfinished index build already exists", nil)
		}
	}

	operationLock, err := acquireIndexLock(indexPaths.directory, true)
	if err != nil {
		return result, classifyRuntime("index_lock_failed", "could not acquire the index operation lock", err)
	}
	defer func() {
		if releaseErr := operationLock.release(); releaseErr != nil && returnErr == nil {
			returnErr = newError(ErrorRuntime, "index_lock_release_failed", "index build completed but its operation lock could not be released", releaseErr)
		}
	}()

	snapshot, err := m.currentSnapshot(ctx, true)
	if err != nil {
		return result, newError(ErrorRuntime, "repository_identity_failed", "could not compute the repository identity", err)
	}
	if err := m.requireStableManagedSnapshot(ctx, snapshot); err != nil {
		return result, err
	}
	indexedAt := started.Format(time.RFC3339Nano)
	buildID, err := newBuildID(started)
	if err != nil {
		return result, newError(ErrorRuntime, "index_build_id_failed", "could not generate an index build ID", err)
	}
	documents, err := m.scanDocuments(ctx, indexedAt)
	if err != nil {
		return result, err
	}

	temporary, err := createTemporaryIndex(indexPaths.directory)
	if err != nil {
		return result, newError(ErrorRuntime, "index_temporary_failed", "could not create the temporary index", err)
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporary)
			_ = os.Remove(temporary + "-wal")
			_ = os.Remove(temporary + "-shm")
		}
	}()

	db, sqliteVersion, err := openDatabase(ctx, temporary, openReadWrite, "DELETE")
	if err != nil {
		return result, classifyRuntime("index_open_failed", "could not open the temporary index", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = db.Close()
		}
	}()
	transaction, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, newError(ErrorRuntime, "index_transaction_failed", "could not begin the index build transaction", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	if err := createSchema(ctx, transaction); err != nil {
		return result, classifyRuntime("index_schema_failed", "could not create the index schema", err)
	}
	metadata := map[string]string{
		"index_schema_version": strconv.Itoa(SchemaVersion),
		"lore_version":         m.LoreVersion,
		"repository_identity":  snapshot.identity,
		"indexed_head":         snapshot.head,
		"indexed_branch":       snapshot.branch,
		"indexed_at":           indexedAt,
		"index_build_id":       buildID,
		"fts5_version":         sqliteVersion,
		"build_complete":       "false",
	}
	if err := writeMetadata(ctx, transaction, metadata); err != nil {
		return result, classifyRuntime("index_metadata_failed", "could not write index metadata", err)
	}
	if err := insertDocuments(ctx, transaction, documents); err != nil {
		return result, classifyRuntime("index_population_failed", "could not populate the index", err)
	}
	if _, err := verifyDatabase(ctx, transaction, true, false, snapshot.identity, len(documents)); err != nil {
		return result, classifyRuntime("index_verification_failed", "temporary index verification failed", err)
	}
	if _, err := transaction.ExecContext(ctx, "UPDATE metadata SET value='true' WHERE key='build_complete'"); err != nil {
		return result, classifyRuntime("index_metadata_failed", "could not finalize index metadata", err)
	}
	if err := transaction.Commit(); err != nil {
		return result, classifyRuntime("index_transaction_failed", "could not commit the index build transaction", err)
	}
	committed = true
	if err := db.Close(); err != nil {
		return result, newError(ErrorRuntime, "index_close_failed", "could not close the temporary index", err)
	}
	closed = true
	if err := syncFile(temporary); err != nil {
		return result, newError(ErrorRuntime, "index_flush_failed", "could not flush the temporary index", err)
	}
	verifiedDB, _, err := openDatabase(ctx, temporary, openReadWrite, "DELETE")
	if err != nil {
		return result, classifyRuntime("index_verification_failed", "could not reopen the temporary index for verification", err)
	}
	verification, verifyErr := verifyDatabase(ctx, verifiedDB, true, true, snapshot.identity, len(documents))
	closeErr := verifiedDB.Close()
	if verifyErr != nil {
		return result, classifyRuntime("index_verification_failed", "temporary index verification failed", verifyErr)
	}
	if closeErr != nil {
		return result, newError(ErrorRuntime, "index_close_failed", "could not close the verified temporary index", closeErr)
	}
	if err := m.requireStableManagedSnapshot(ctx, snapshot); err != nil {
		return result, err
	}
	if err := prepareExistingIndexForReplacement(ctx, indexPaths); err != nil {
		return result, err
	}
	if err := os.Rename(temporary, indexPaths.live); err != nil {
		return result, newError(ErrorRuntime, "index_replace_failed", "could not atomically install the verified index", err)
	}
	removeTemporary = false
	if err := os.Chmod(indexPaths.live, 0o600); err != nil {
		return result, newError(ErrorRuntime, "index_permissions_failed", "the index was installed but its permissions could not be restricted", err)
	}
	syncDirectory(indexPaths.directory)

	liveDB, _, err := openDatabase(ctx, indexPaths.live, openReadWrite, "WAL")
	if err != nil {
		return result, classifyRuntime("index_live_open_failed", "the verified index was installed but WAL mode could not be enabled", err)
	}
	if _, err := verifyDatabase(ctx, liveDB, false, true, snapshot.identity, len(documents)); err != nil {
		_ = liveDB.Close()
		return result, classifyRuntime("index_verification_failed", "the installed index failed verification", err)
	}
	if err := liveDB.Close(); err != nil {
		return result, newError(ErrorRuntime, "index_close_failed", "could not close the installed index", err)
	}

	state := StateUncertified
	if snapshot.isGit && snapshot.head != "" {
		state = StateFresh
	}
	duration := m.Clock.Now().UTC().Sub(started).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	return BuildResult{
		SchemaVersion: SchemaVersion,
		Status:        "ok",
		IndexState:    state,
		Path:          RelativeIndexPath,
		DocumentCount: verification.documentCount,
		PageCount:     verification.pageCount,
		SourceCount:   verification.sourceCount,
		IndexedHead:   snapshot.head,
		IndexedBranch: snapshot.branch,
		IndexedAt:     indexedAt,
		IndexBuildID:  buildID,
		DurationMS:    duration,
		Warnings:      []Warning{},
	}, nil
}

func createTemporaryIndex(directory string) (string, error) {
	file, err := os.CreateTemp(directory, "index.build.*.sqlite")
	if err != nil {
		return "", fmt.Errorf("create temporary index: %w", err)
	}
	path := file.Name()
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(path)
	}
	if err := file.Chmod(0o600); err != nil {
		cleanup()
		return "", fmt.Errorf("restrict temporary index permissions: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close temporary index: %w", err)
	}
	return path, nil
}

func insertDocuments(ctx context.Context, transaction *sql.Tx, documents []indexedDocument) error {
	documentStatement, err := transaction.PrepareContext(ctx, `
INSERT INTO documents(
    path, document_id, document_type, title, kind, sensitivity,
    aliases_text, tags_text, aliases_json, tags_json, body, body_line_start, revision,
    content_sha256, created_at, updated_at, indexed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?)
`)
	if err != nil {
		return fmt.Errorf("prepare document insert: %w", err)
	}
	defer documentStatement.Close()
	ftsStatement, err := transaction.PrepareContext(ctx, `
INSERT INTO documents_fts(rowid, title, aliases_text, tags_text, path, body)
VALUES (?, ?, ?, ?, ?, ?)
`)
	if err != nil {
		return fmt.Errorf("prepare FTS insert: %w", err)
	}
	defer ftsStatement.Close()
	for _, document := range documents {
		result, err := documentStatement.ExecContext(
			ctx,
			document.Path,
			document.DocumentID,
			document.DocumentType,
			document.Title,
			document.Kind,
			document.Sensitivity,
			document.AliasesText,
			document.TagsText,
			document.AliasesJSON,
			document.TagsJSON,
			document.Body,
			document.BodyLineStart,
			document.Revision,
			document.ContentSHA256,
			document.CreatedAt,
			document.UpdatedAt,
			document.IndexedAt,
		)
		if err != nil {
			return fmt.Errorf("insert document %s: %w", document.Path, err)
		}
		rowID, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("read inserted document row ID: %w", err)
		}
		if _, err := ftsStatement.ExecContext(
			ctx,
			rowID,
			document.Title,
			document.AliasesText,
			document.TagsText,
			document.Path,
			document.Body,
		); err != nil {
			return fmt.Errorf("insert FTS document %s: %w", document.Path, err)
		}
	}
	return nil
}

func prepareExistingIndexForReplacement(ctx context.Context, indexPaths paths) error {
	info, err := os.Lstat(indexPaths.live)
	if errors.Is(err, fs.ErrNotExist) {
		return removeCompanions(indexPaths)
	}
	if err != nil {
		return newError(ErrorRuntime, "index_replace_failed", "could not inspect the existing index", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return newError(ErrorRuntime, "unsafe_index_path", "existing index is not a regular non-symlink file", nil)
	}
	existing, _, openErr := openDatabase(ctx, indexPaths.live, openReadWrite, "")
	if openErr == nil {
		var busy, logFrames, checkpointed int
		checkpointErr := existing.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed)
		if checkpointErr == nil && busy != 0 {
			checkpointErr = fmt.Errorf("existing index has active SQLite readers")
		}
		if checkpointErr == nil {
			var journalMode string
			checkpointErr = existing.QueryRowContext(ctx, "PRAGMA journal_mode = DELETE").Scan(&journalMode)
			if checkpointErr == nil && journalMode != "delete" {
				checkpointErr = fmt.Errorf("existing index retained journal mode %s", journalMode)
			}
		}
		closeErr := existing.Close()
		if checkpointErr != nil {
			return newError(ErrorConflict, "index_busy", "existing index could not be checkpointed for replacement", checkpointErr)
		}
		if closeErr != nil {
			return newError(ErrorRuntime, "index_close_failed", "could not close the existing index", closeErr)
		}
	}
	return removeCompanions(indexPaths)
}

func removeCompanions(indexPaths paths) error {
	for _, path := range []string{indexPaths.live + "-wal", indexPaths.live + "-shm"} {
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return newError(ErrorRuntime, "index_companion_failed", "could not inspect an index companion file", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return newError(ErrorRuntime, "unsafe_index_path", "index companion path is not a regular non-symlink file", nil)
		}
		if err := os.Remove(path); err != nil {
			return newError(ErrorRuntime, "index_companion_failed", "could not remove a checkpointed index companion file", err)
		}
	}
	return nil
}

func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func newBuildID(now time.Time) (string, error) {
	value, err := ulid.New(ulid.Timestamp(now.UTC()), rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate index build ULID: %w", err)
	}
	return "idx_" + value.String(), nil
}

func classifyRuntime(code, message string, err error) error {
	var indexErr *Error
	if errors.As(err, &indexErr) {
		return indexErr
	}
	if errors.Is(err, ErrFTS5Unsupported) {
		return newError(ErrorRuntime, "fts5_unsupported", "the linked SQLite build does not support FTS5", err)
	}
	return newError(ErrorRuntime, code, message, err)
}
