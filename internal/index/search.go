package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"lore/internal/catalog"
	"lore/internal/docs"
	"lore/internal/search"
)

func (m *Manager) IndexSearchStatus(ctx context.Context, verifyManifest bool) (search.IndexedStatus, error) {
	status, err := m.Status(ctx, false)
	if err != nil {
		return search.IndexedStatus{}, err
	}
	manifestMatches := status.ManifestMatches
	if verifyManifest && status.IndexState == StateUncertified {
		indexPaths, err := resolvePaths(m.Repo, false)
		if err != nil {
			return search.IndexedStatus{}, newError(ErrorRuntime, "unsafe_index_path", "could not resolve the index path", err)
		}
		operationLock, err := acquireIndexLock(indexPaths.directory, false)
		if err != nil {
			return search.IndexedStatus{}, classifyRuntime("index_lock_failed", "could not acquire the index operation lock", err)
		}
		defer operationLock.release()
		db, _, err := openDatabase(ctx, indexPaths.live, openReadOnly, "")
		if err != nil {
			return search.IndexedStatus{}, classifyRuntime("index_open_failed", "could not open the index for manifest verification", err)
		}
		manifestMatches, err = m.manifestMatches(ctx, db)
		closeErr := db.Close()
		if err != nil {
			return search.IndexedStatus{}, newError(ErrorRuntime, "manifest_verification_failed", "could not verify the non-Git canonical manifest", err)
		}
		if closeErr != nil {
			return search.IndexedStatus{}, newError(ErrorRuntime, "index_close_failed", "could not close the index after manifest verification", closeErr)
		}
	}
	return search.IndexedStatus{
		State:           string(status.IndexState),
		ManifestMatches: manifestMatches,
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
	if err != nil || schemaVersion != IndexSchemaVersion || metadata["build_complete"] != "true" {
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

	filterSQL, filterArguments, allowed, err := candidateFilters(request)
	if err != nil {
		return batch, err
	}
	if !allowed {
		return search.CandidateBatch{Documents: []search.Candidate{}}, nil
	}
	if err := db.QueryRowContext(
		ctx,
		"SELECT count(*) FROM documents AS d WHERE 1=1"+filterSQL,
		filterArguments...,
	).Scan(&batch.CorpusSize); err != nil {
		return batch, newError(ErrorRuntime, "index_search_failed", "could not count the filtered search corpus", err)
	}
	batch.DocumentFrequency = make(map[string]int, len(request.QueryTokens))
	for _, token := range request.QueryTokens {
		if token == "" {
			continue
		}
		frequencyArguments := append([]any{token}, filterArguments...)
		var frequency int
		if err := db.QueryRowContext(
			ctx,
			`SELECT count(*)
FROM document_terms AS t
JOIN documents AS d ON d.rowid=t.document_rowid
WHERE t.term=?`+filterSQL,
			frequencyArguments...,
		).Scan(&frequency); err != nil {
			return batch, newError(ErrorRuntime, "index_search_failed", "could not compute lexical document frequency", err)
		}
		batch.DocumentFrequency[token] = frequency
	}
	plan, err := search.PlanFuzzyExpansion(request.QueryTokens, request.Matching, batch.DocumentFrequency)
	if err != nil {
		return batch, err
	}
	if plan.Truncated {
		batch.Warnings = append(batch.Warnings, catalog.Warning{
			Code:    "fuzzy_token_limit",
			Path:    ".lore/index.sqlite",
			Message: "automatic fuzzy matching considered the eight longest eligible query terms",
		})
	}
	if len(plan.Targets) > 0 {
		vocabulary, limitReached, err := indexedFuzzyVocabulary(ctx, db, plan, filterSQL, filterArguments)
		if err != nil {
			return batch, err
		}
		if limitReached {
			if search.NormalizeMatching(request.Matching) == search.MatchingFuzzy {
				return batch, &search.MatchingError{
					Code:    "fuzzy_vocabulary_limit",
					Message: fmt.Sprintf("filtered fuzzy vocabulary exceeds the supported bound of %d terms", search.MaximumFuzzyVocabularyTerms),
				}
			}
			batch.Warnings = append(batch.Warnings, catalog.Warning{
				Code:    "fuzzy_vocabulary_limit",
				Path:    ".lore/index.sqlite",
				Message: "automatic fuzzy expansion was skipped because the filtered vocabulary exceeded its work bound",
			})
		} else {
			batch.Expansions = search.SelectFuzzyExpansions(plan, vocabulary)
			for _, expansion := range batch.Expansions {
				batch.DocumentFrequency[expansion.DocumentTerm] = expansion.DocumentFrequency
			}
		}
	}

	query := strings.Builder{}
	query.WriteString(`
SELECT d.path, d.document_id, d.document_type, d.title, d.kind, d.sensitivity,
       d.aliases_json, d.tags_json, d.body, d.body_line_start, d.revision
FROM documents_fts
JOIN documents AS d ON d.rowid=documents_fts.rowid
WHERE documents_fts MATCH ?`)
	query.WriteString(filterSQL)
	matchExpression := search.ExtendMatchExpression(request.MatchExpression, batch.Expansions)
	arguments := append([]any{matchExpression}, filterArguments...)
	query.WriteString(" ORDER BY bm25(documents_fts, 8.0, 4.0, 3.0, 2.0, 1.0, 1.0), d.path LIMIT ?")
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

func indexedFuzzyVocabulary(
	ctx context.Context,
	db interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	},
	plan search.FuzzyPlan,
	filterSQL string,
	filterArguments []any,
) ([]search.VocabularyTerm, bool, error) {
	lengths := search.FuzzyCandidateLengths(plan)
	query := strings.Builder{}
	query.WriteString(`
SELECT t.term, count(*)
FROM document_terms AS t
JOIN documents AS d ON d.rowid=t.document_rowid
WHERE t.rune_length IN (`)
	arguments := make([]any, 0, len(lengths)+len(filterArguments)+1)
	for index, length := range lengths {
		if index > 0 {
			query.WriteByte(',')
		}
		query.WriteByte('?')
		arguments = append(arguments, length)
	}
	query.WriteByte(')')
	query.WriteString(filterSQL)
	query.WriteString(" GROUP BY t.term ORDER BY t.term LIMIT ?")
	arguments = append(arguments, filterArguments...)
	arguments = append(arguments, search.MaximumFuzzyVocabularyTerms+1)
	rows, err := db.QueryContext(ctx, query.String(), arguments...)
	if err != nil {
		return nil, false, newError(ErrorRuntime, "index_search_failed", "could not read the filtered fuzzy vocabulary", err)
	}
	defer rows.Close()
	vocabulary := make([]search.VocabularyTerm, 0)
	for rows.Next() {
		var term search.VocabularyTerm
		if err := rows.Scan(&term.Term, &term.DocumentFrequency); err != nil {
			return nil, false, newError(ErrorRuntime, "index_search_failed", "could not decode the filtered fuzzy vocabulary", err)
		}
		vocabulary = append(vocabulary, term)
	}
	if err := rows.Err(); err != nil {
		return nil, false, newError(ErrorRuntime, "index_search_failed", "could not read the filtered fuzzy vocabulary", err)
	}
	if len(vocabulary) > search.MaximumFuzzyVocabularyTerms {
		return nil, true, nil
	}
	return vocabulary, false, nil
}

func candidateFilters(request search.CandidateRequest) (string, []any, bool, error) {
	query := strings.Builder{}
	arguments := []any{}
	switch request.Scope {
	case search.ScopePages:
		query.WriteString(" AND d.document_type='page'")
	case search.ScopeSources:
		query.WriteString(" AND d.document_type='source'")
	case "", search.ScopeAll:
	default:
		return "", nil, false, newError(ErrorUsage, "invalid_scope", "index candidate scope is invalid", nil)
	}
	if request.Kind != "" {
		query.WriteString(" AND d.kind=?")
		arguments = append(arguments, request.Kind)
	}
	tags := append([]string(nil), request.Tags...)
	sort.Strings(tags)
	for _, tag := range tags {
		query.WriteString(" AND EXISTS (SELECT 1 FROM json_each(d.tags_json) WHERE value=?)")
		arguments = append(arguments, tag)
	}
	paths := append([]string(nil), request.Paths...)
	sort.Strings(paths)
	if len(paths) > 0 {
		query.WriteString(" AND (")
		for index, path := range paths {
			if index > 0 {
				query.WriteString(" OR ")
			}
			path = strings.TrimSuffix(path, "/")
			query.WriteString("(d.path=? OR d.path LIKE ? ESCAPE '\\')")
			arguments = append(arguments, path, escapeLike(path)+"/%")
		}
		query.WriteByte(')')
	}
	sensitivities := append([]string(nil), request.AllowedSensitivities...)
	sort.Strings(sensitivities)
	if len(sensitivities) == 0 {
		return "", nil, false, nil
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
	return query.String(), arguments, true, nil
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}
