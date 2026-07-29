package index

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"lore/internal/docs"
	"lore/internal/search"
)

func (m *Manager) IndexSearchStatus(ctx context.Context, verifyManifest bool) (search.IndexedStatus, error) {
	status, err := m.Status(ctx, verifyManifest)
	if err != nil {
		return search.IndexedStatus{}, err
	}
	return search.IndexedStatus{
		State:           string(status.IndexState),
		ManifestMatches: status.ManifestMatches,
	}, nil
}

func (m *Manager) IndexCandidates(ctx context.Context, request search.CandidateRequest) (batch search.CandidateBatch, returnErr error) {
	if m == nil || m.Repo == nil || m.Git == nil {
		return batch, newError(ErrorRuntime, "index_unavailable", "index manager is not fully configured", nil)
	}
	if request.MatchExpression == "" {
		return batch, newError(ErrorUsage, "invalid_index_query", "index MATCH expression must not be empty", nil)
	}
	if request.Limit < 1 || request.Limit > 100_000 {
		return batch, newError(ErrorUsage, "invalid_candidate_limit", "index candidate limit is outside the supported range", nil)
	}
	indexPaths, err := resolvePaths(m.Repo, false)
	if err != nil {
		return batch, newError(ErrorRuntime, "unsafe_index_path", "could not resolve the index path", err)
	}
	operationLock, err := acquireIndexLock(indexPaths.directory, false)
	if err != nil {
		return batch, classifyRuntime("index_lock_failed", "could not acquire the index operation lock", err)
	}
	defer func() {
		if releaseErr := operationLock.release(); releaseErr != nil && returnErr == nil {
			returnErr = newError(ErrorRuntime, "index_lock_release_failed", "indexed search completed but its operation lock could not be released", releaseErr)
		}
	}()
	db, _, err := openDatabase(ctx, indexPaths.live, openReadOnly, "")
	if err != nil {
		return batch, classifyRuntime("index_open_failed", "could not open the index for search", err)
	}
	defer db.Close()

	metadata, err := readMetadata(ctx, db)
	if err != nil {
		return batch, classifyRuntime("index_metadata_failed", "could not read index metadata", err)
	}
	schemaVersion, err := metadataSchemaVersion(metadata)
	if err != nil || schemaVersion != SchemaVersion || metadata["build_complete"] != "true" {
		return batch, newError(ErrorRuntime, "index_incompatible", "the index is incomplete or incompatible", err)
	}
	snapshot, err := m.currentSnapshot(ctx, false)
	if err != nil {
		return batch, newError(ErrorRuntime, "repository_identity_failed", "could not verify the repository identity", err)
	}
	if metadata["repository_identity"] != snapshot.identity {
		return batch, newError(ErrorRuntime, "repository_identity_mismatch", "the index belongs to a different repository", nil)
	}
	if snapshot.isGit && snapshot.head != "" {
		if metadata["indexed_head"] != snapshot.head || metadata["indexed_branch"] != snapshot.branch {
			return batch, newError(ErrorConflict, "index_not_fresh", "the index Git snapshot is stale", nil)
		}
		if err := m.requireStableManagedSnapshot(ctx, snapshot); err != nil {
			return batch, err
		}
		recoveryActive, err := recoveryExists(m.Repo)
		if err != nil {
			return batch, newError(ErrorRuntime, "recovery_check_failed", "could not inspect transaction recovery state", err)
		}
		if recoveryActive {
			return batch, newError(ErrorConflict, "index_not_fresh", "transaction recovery makes the index stale", nil)
		}
	} else {
		matches, err := m.manifestMatches(ctx, db)
		if err != nil {
			return batch, newError(ErrorRuntime, "manifest_verification_failed", "could not verify the non-Git canonical manifest", err)
		}
		if !matches {
			return batch, newError(ErrorConflict, "index_not_fresh", "the non-Git canonical manifest differs from the index", nil)
		}
	}

	query := strings.Builder{}
	query.WriteString(`
SELECT d.path, d.document_id, d.document_type, d.title, d.kind, d.sensitivity,
       d.aliases_json, d.tags_json, d.body, d.body_line_start, d.revision
FROM documents_fts
JOIN documents AS d ON d.rowid=documents_fts.rowid
WHERE documents_fts MATCH ?`)
	arguments := []any{request.MatchExpression}
	switch request.Scope {
	case search.ScopePages:
		query.WriteString(" AND d.document_type='page'")
	case search.ScopeSources:
		query.WriteString(" AND d.document_type='source'")
	case "", search.ScopeAll:
	default:
		return batch, newError(ErrorUsage, "invalid_scope", "index candidate scope is invalid", nil)
	}
	if request.Kind != "" {
		query.WriteString(" AND d.kind=?")
		arguments = append(arguments, request.Kind)
	}
	sensitivities := append([]string(nil), request.AllowedSensitivities...)
	sort.Strings(sensitivities)
	if len(sensitivities) == 0 {
		return search.CandidateBatch{Documents: []search.Candidate{}}, nil
	}
	query.WriteString(" AND d.sensitivity IN (")
	for index := range sensitivities {
		if index > 0 {
			query.WriteByte(',')
		}
		query.WriteByte('?')
		arguments = append(arguments, sensitivities[index])
	}
	query.WriteByte(')')
	query.WriteString(" ORDER BY bm25(documents_fts, 8.0, 4.0, 3.0, 1.0, 1.0), d.path LIMIT ?")
	arguments = append(arguments, request.Limit+1)

	rows, err := db.QueryContext(ctx, query.String(), arguments...)
	if err != nil {
		return batch, newError(ErrorRuntime, "index_search_failed", "FTS5 candidate lookup failed", err)
	}
	defer rows.Close()
	candidates := make([]search.Candidate, 0, request.Limit+1)
	for rows.Next() {
		var candidate search.Candidate
		var documentType, aliasesJSON, tagsJSON, body string
		if err := rows.Scan(
			&candidate.Path,
			&candidate.DocumentID,
			&documentType,
			&candidate.Title,
			&candidate.Kind,
			&candidate.Sensitivity,
			&aliasesJSON,
			&tagsJSON,
			&body,
			&candidate.BodyLineStart,
			&candidate.Revision,
		); err != nil {
			return batch, newError(ErrorRuntime, "index_search_failed", "could not decode an indexed candidate", err)
		}
		candidate.DocumentType = docs.Type(documentType)
		if candidate.DocumentType != docs.TypePage && candidate.DocumentType != docs.TypeSource {
			return batch, newError(ErrorRuntime, "index_corrupt", "an indexed candidate has an invalid document type", nil)
		}
		if err := json.Unmarshal([]byte(aliasesJSON), &candidate.Aliases); err != nil {
			return batch, newError(ErrorRuntime, "index_corrupt", "an indexed candidate has malformed alias metadata", err)
		}
		if err := json.Unmarshal([]byte(tagsJSON), &candidate.Tags); err != nil {
			return batch, newError(ErrorRuntime, "index_corrupt", "an indexed candidate has malformed tag metadata", err)
		}
		candidate.Body = []byte(body)
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return batch, newError(ErrorRuntime, "index_search_failed", "FTS5 candidate lookup failed", err)
	}
	if snapshot.isGit && snapshot.head != "" {
		if err := m.requireStableManagedSnapshot(ctx, snapshot); err != nil {
			return batch, err
		}
	} else {
		matches, err := m.manifestMatches(ctx, db)
		if err != nil || !matches {
			return batch, newError(ErrorConflict, "index_not_fresh", "the non-Git canonical manifest changed during search", err)
		}
	}
	if len(candidates) > request.Limit {
		batch.LimitReached = true
		candidates = candidates[:request.Limit]
	}
	batch.Documents = candidates
	return batch, nil
}
