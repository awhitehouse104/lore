package index

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"time"
)

func (m *Manager) Update(ctx context.Context) (result UpdateResult, returnErr error) {
	if m == nil || m.Repo == nil || m.Git == nil || m.Clock == nil {
		return result, newError(ErrorRuntime, "index_unavailable", "index manager is not fully configured", nil)
	}
	started := m.Clock.Now().UTC()
	indexPaths, err := resolvePaths(m.Repo, false)
	if err != nil {
		return result, newError(ErrorRuntime, "unsafe_index_path", "could not resolve the index path", err)
	}
	currentStatus, err := m.Status(ctx, false)
	if err != nil {
		return result, err
	}
	switch currentStatus.IndexState {
	case StateMissing:
		return result, newError(ErrorRuntime, "index_missing", "no index exists; run lore index build", nil)
	case StateBuilding:
		return result, newError(ErrorConflict, "index_building", "an index build or exclusive operation is active", nil)
	case StateCorrupt:
		return result, newError(ErrorRuntime, "index_corrupt", "the index is corrupt and must be rebuilt", nil)
	case StateIncompatible:
		return result, newError(ErrorRuntime, "index_incompatible", "the index is incompatible and must be rebuilt", nil)
	}

	operationLock, err := acquireIndexLock(indexPaths.directory, false)
	if err != nil {
		return result, classifyRuntime("index_lock_failed", "could not acquire the index operation lock", err)
	}
	defer func() {
		if releaseErr := operationLock.release(); releaseErr != nil && returnErr == nil {
			returnErr = newError(ErrorRuntime, "index_lock_release_failed", "index update completed but its operation lock could not be released", releaseErr)
		}
	}()

	snapshot, err := m.currentSnapshot(ctx, false)
	if err != nil {
		return result, newError(ErrorRuntime, "repository_identity_failed", "could not compute the repository identity", err)
	}
	if err := m.requireStableManagedSnapshot(ctx, snapshot); err != nil {
		return result, err
	}
	indexedAt := started.Format(time.RFC3339Nano)
	buildID, err := newBuildID(started)
	if err != nil {
		return result, newError(ErrorRuntime, "index_build_id_failed", "could not generate an index update ID", err)
	}
	currentDocuments, err := m.scanDocuments(ctx, indexedAt)
	if err != nil {
		return result, err
	}

	db, sqliteVersion, err := openDatabase(ctx, indexPaths.live, openReadWrite, "WAL")
	if err != nil {
		return result, classifyRuntime("index_open_failed", "could not open the index for update", err)
	}
	defer db.Close()
	transaction, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, newError(ErrorRuntime, "index_transaction_failed", "could not begin the index update transaction", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()

	metadata, err := readMetadata(ctx, transaction)
	if err != nil {
		return result, classifyRuntime("index_metadata_failed", "could not read index metadata", err)
	}
	schemaVersion, err := metadataSchemaVersion(metadata)
	if err != nil || schemaVersion != SchemaVersion {
		return result, newError(ErrorRuntime, "index_incompatible", "the index schema is incompatible and must be rebuilt", err)
	}
	if metadata["repository_identity"] != snapshot.identity {
		return result, newError(ErrorRuntime, "repository_identity_mismatch", "the index belongs to a different repository", nil)
	}
	storedDocuments, err := loadStoredDocuments(ctx, transaction)
	if err != nil {
		return result, classifyRuntime("index_read_failed", "could not read existing index rows", err)
	}

	statements, err := prepareUpdateStatements(ctx, transaction)
	if err != nil {
		return result, classifyRuntime("index_update_failed", "could not prepare index reconciliation", err)
	}
	defer statements.close()
	currentPaths := make(map[string]struct{}, len(currentDocuments))
	for _, document := range currentDocuments {
		currentPaths[document.Path] = struct{}{}
		stored, exists := storedDocuments[document.Path]
		if !exists {
			if err := statements.insert(ctx, document); err != nil {
				return result, classifyRuntime("index_update_failed", "could not add an indexed document", err)
			}
			result.Added++
			continue
		}
		if stored.Revision == document.Revision {
			result.Unchanged++
			continue
		}
		if err := statements.replace(ctx, stored, document); err != nil {
			return result, classifyRuntime("index_update_failed", "could not update an indexed document", err)
		}
		result.Updated++
	}
	storedPaths := make([]string, 0, len(storedDocuments))
	for path := range storedDocuments {
		storedPaths = append(storedPaths, path)
	}
	sort.Strings(storedPaths)
	for _, path := range storedPaths {
		if _, exists := currentPaths[path]; exists {
			continue
		}
		if err := statements.delete(ctx, storedDocuments[path]); err != nil {
			return result, classifyRuntime("index_update_failed", "could not delete an indexed document", err)
		}
		result.Deleted++
	}

	nextMetadata := map[string]string{
		"index_schema_version": strconv.Itoa(SchemaVersion),
		"lore_version":         m.LoreVersion,
		"repository_identity":  snapshot.identity,
		"indexed_head":         snapshot.head,
		"indexed_branch":       snapshot.branch,
		"indexed_at":           indexedAt,
		"index_build_id":       buildID,
		"fts5_version":         sqliteVersion,
		"build_complete":       "true",
	}
	if err := writeMetadata(ctx, transaction, nextMetadata); err != nil {
		return result, classifyRuntime("index_metadata_failed", "could not update index metadata", err)
	}
	if _, err := verifyDatabase(ctx, transaction, true, true, snapshot.identity, len(currentDocuments)); err != nil {
		return result, classifyRuntime("index_verification_failed", "updated index verification failed", err)
	}
	if err := m.requireStableManagedSnapshot(ctx, snapshot); err != nil {
		return result, err
	}
	if err := transaction.Commit(); err != nil {
		return result, newError(ErrorRuntime, "index_transaction_failed", "could not commit the index update transaction", err)
	}
	committed = true

	state := StateUncertified
	if snapshot.isGit && snapshot.head != "" {
		state = StateFresh
	}
	duration := m.Clock.Now().UTC().Sub(started).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	result.SchemaVersion = SchemaVersion
	result.Status = "ok"
	result.IndexState = state
	result.IndexedHead = snapshot.head
	result.IndexedBranch = snapshot.branch
	result.IndexedAt = indexedAt
	result.IndexBuildID = buildID
	result.DurationMS = duration
	result.Warnings = []Warning{}
	return result, nil
}

type storedDocument struct {
	RowID int64
	indexedDocument
}

func loadStoredDocuments(ctx context.Context, transaction *sql.Tx) (map[string]storedDocument, error) {
	rows, err := transaction.QueryContext(ctx, `
SELECT rowid, path, document_id, document_type, title, kind, sensitivity,
       aliases_text, tags_text, body, body_line_start, revision,
       content_sha256, COALESCE(created_at, ''), COALESCE(updated_at, ''), indexed_at
FROM documents
ORDER BY path
`)
	if err != nil {
		return nil, fmt.Errorf("query indexed documents: %w", err)
	}
	defer rows.Close()
	documents := map[string]storedDocument{}
	for rows.Next() {
		var document storedDocument
		if err := rows.Scan(
			&document.RowID,
			&document.Path,
			&document.DocumentID,
			&document.DocumentType,
			&document.Title,
			&document.Kind,
			&document.Sensitivity,
			&document.AliasesText,
			&document.TagsText,
			&document.Body,
			&document.BodyLineStart,
			&document.Revision,
			&document.ContentSHA256,
			&document.CreatedAt,
			&document.UpdatedAt,
			&document.IndexedAt,
		); err != nil {
			return nil, fmt.Errorf("decode indexed document: %w", err)
		}
		documents[document.Path] = document
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query indexed documents: %w", err)
	}
	return documents, nil
}

type updateStatements struct {
	insertDocument *sql.Stmt
	updateDocument *sql.Stmt
	deleteDocument *sql.Stmt
	insertFTS      *sql.Stmt
	deleteFTS      *sql.Stmt
}

func prepareUpdateStatements(ctx context.Context, transaction *sql.Tx) (*updateStatements, error) {
	queries := []string{
		`INSERT INTO documents(
		    path, document_id, document_type, title, kind, sensitivity,
		    aliases_text, tags_text, body, body_line_start, revision,
		    content_sha256, created_at, updated_at, indexed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?)`,
		`UPDATE documents SET
		    document_id=?, document_type=?, title=?, kind=?, sensitivity=?,
		    aliases_text=?, tags_text=?, body=?, body_line_start=?, revision=?,
		    content_sha256=?, created_at=NULLIF(?, ''), updated_at=NULLIF(?, ''), indexed_at=?
		WHERE rowid=?`,
		"DELETE FROM documents WHERE rowid=?",
		`INSERT INTO documents_fts(rowid, title, aliases_text, tags_text, path, body)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		`INSERT INTO documents_fts(documents_fts, rowid, title, aliases_text, tags_text, path, body)
		 VALUES ('delete', ?, ?, ?, ?, ?, ?)`,
	}
	prepared := make([]*sql.Stmt, 0, len(queries))
	for _, query := range queries {
		statement, err := transaction.PrepareContext(ctx, query)
		if err != nil {
			for _, existing := range prepared {
				_ = existing.Close()
			}
			return nil, err
		}
		prepared = append(prepared, statement)
	}
	return &updateStatements{
		insertDocument: prepared[0],
		updateDocument: prepared[1],
		deleteDocument: prepared[2],
		insertFTS:      prepared[3],
		deleteFTS:      prepared[4],
	}, nil
}

func (s *updateStatements) insert(ctx context.Context, document indexedDocument) error {
	result, err := s.insertDocument.ExecContext(ctx, documentValues(document)...)
	if err != nil {
		return err
	}
	rowID, err := result.LastInsertId()
	if err != nil {
		return err
	}
	_, err = s.insertFTS.ExecContext(ctx, ftsValues(rowID, document)...)
	return err
}

func (s *updateStatements) replace(ctx context.Context, old storedDocument, document indexedDocument) error {
	if _, err := s.deleteFTS.ExecContext(ctx, ftsValues(old.RowID, old.indexedDocument)...); err != nil {
		return err
	}
	values := []any{
		document.DocumentID,
		document.DocumentType,
		document.Title,
		document.Kind,
		document.Sensitivity,
		document.AliasesText,
		document.TagsText,
		document.Body,
		document.BodyLineStart,
		document.Revision,
		document.ContentSHA256,
		document.CreatedAt,
		document.UpdatedAt,
		document.IndexedAt,
		old.RowID,
	}
	if _, err := s.updateDocument.ExecContext(ctx, values...); err != nil {
		return err
	}
	_, err := s.insertFTS.ExecContext(ctx, ftsValues(old.RowID, document)...)
	return err
}

func (s *updateStatements) delete(ctx context.Context, document storedDocument) error {
	if _, err := s.deleteFTS.ExecContext(ctx, ftsValues(document.RowID, document.indexedDocument)...); err != nil {
		return err
	}
	_, err := s.deleteDocument.ExecContext(ctx, document.RowID)
	return err
}

func (s *updateStatements) close() {
	for _, statement := range []*sql.Stmt{s.insertDocument, s.updateDocument, s.deleteDocument, s.insertFTS, s.deleteFTS} {
		if statement != nil {
			_ = statement.Close()
		}
	}
}

func documentValues(document indexedDocument) []any {
	return []any{
		document.Path,
		document.DocumentID,
		document.DocumentType,
		document.Title,
		document.Kind,
		document.Sensitivity,
		document.AliasesText,
		document.TagsText,
		document.Body,
		document.BodyLineStart,
		document.Revision,
		document.ContentSHA256,
		document.CreatedAt,
		document.UpdatedAt,
		document.IndexedAt,
	}
}

func ftsValues(rowID int64, document indexedDocument) []any {
	return []any{
		rowID,
		document.Title,
		document.AliasesText,
		document.TagsText,
		document.Path,
		document.Body,
	}
}
