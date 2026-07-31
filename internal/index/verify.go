package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"lore/internal/search"
)

type sqlRunner interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type verificationResult struct {
	metadata        map[string]string
	documentCount   int
	pageCount       int
	sourceCount     int
	secureDelete    bool
	ftsSecureDelete bool
}

func verifyDatabase(
	ctx context.Context,
	runner sqlRunner,
	full bool,
	expectedComplete bool,
	expectedIdentity string,
	expectedDocuments int,
) (verificationResult, error) {
	check := "PRAGMA quick_check"
	if full {
		check = "PRAGMA integrity_check"
	}
	rows, err := runner.QueryContext(ctx, check)
	if err != nil {
		return verificationResult{}, fmt.Errorf("check index integrity: %w", err)
	}
	var integrityMessages []string
	for rows.Next() {
		var message string
		if err := rows.Scan(&message); err != nil {
			_ = rows.Close()
			return verificationResult{}, fmt.Errorf("decode index integrity result: %w", err)
		}
		if message != "ok" {
			integrityMessages = append(integrityMessages, message)
		}
	}
	if err := rows.Close(); err != nil {
		return verificationResult{}, fmt.Errorf("close index integrity result: %w", err)
	}
	if len(integrityMessages) > 0 {
		return verificationResult{}, fmt.Errorf("index integrity check reported %d problem(s)", len(integrityMessages))
	}

	metadata, err := readMetadata(ctx, runner)
	if err != nil {
		return verificationResult{}, err
	}
	schemaVersion, err := metadataSchemaVersion(metadata)
	if err != nil {
		return verificationResult{}, err
	}
	if schemaVersion != IndexSchemaVersion {
		return verificationResult{}, fmt.Errorf("index schema version %d is incompatible with supported version %d", schemaVersion, IndexSchemaVersion)
	}
	complete := metadata["build_complete"] == "true"
	if complete != expectedComplete {
		return verificationResult{}, fmt.Errorf("index build_complete metadata is inconsistent")
	}
	if expectedIdentity != "" && metadata["repository_identity"] != expectedIdentity {
		return verificationResult{}, fmt.Errorf("index repository identity does not match")
	}

	result := verificationResult{metadata: metadata}
	if err := runner.QueryRowContext(ctx, "SELECT count(*) FROM documents").Scan(&result.documentCount); err != nil {
		return verificationResult{}, fmt.Errorf("count indexed documents: %w", err)
	}
	if expectedDocuments >= 0 && result.documentCount != expectedDocuments {
		return verificationResult{}, fmt.Errorf("index contains %d documents; expected %d", result.documentCount, expectedDocuments)
	}
	var documentsWithoutTerms int
	if err := runner.QueryRowContext(ctx, `
SELECT count(*)
FROM documents AS d
WHERE NOT EXISTS (SELECT 1 FROM document_terms AS t WHERE t.document_rowid=d.rowid)
`).Scan(&documentsWithoutTerms); err != nil {
		return verificationResult{}, fmt.Errorf("verify exact lexical terms: %w", err)
	}
	if documentsWithoutTerms != 0 {
		return verificationResult{}, fmt.Errorf("index contains %d document(s) without exact lexical terms", documentsWithoutTerms)
	}
	var orphanTerms int
	if err := runner.QueryRowContext(ctx, `
SELECT count(*)
FROM document_terms AS t
LEFT JOIN documents AS d ON d.rowid=t.document_rowid
WHERE d.rowid IS NULL
`).Scan(&orphanTerms); err != nil {
		return verificationResult{}, fmt.Errorf("verify exact lexical term ownership: %w", err)
	}
	if orphanTerms != 0 {
		return verificationResult{}, fmt.Errorf("index contains %d orphan exact lexical term(s)", orphanTerms)
	}
	if full {
		if err := verifyExactTermContents(ctx, runner); err != nil {
			return verificationResult{}, err
		}
	}
	if err := runner.QueryRowContext(ctx, "SELECT count(*) FROM documents WHERE document_type='page'").Scan(&result.pageCount); err != nil {
		return verificationResult{}, fmt.Errorf("count indexed pages: %w", err)
	}
	if err := runner.QueryRowContext(ctx, "SELECT count(*) FROM documents WHERE document_type='source'").Scan(&result.sourceCount); err != nil {
		return verificationResult{}, fmt.Errorf("count indexed sources: %w", err)
	}
	var duplicatePaths int
	if err := runner.QueryRowContext(ctx, "SELECT count(*) FROM (SELECT path FROM documents GROUP BY path HAVING count(*) > 1)").Scan(&duplicatePaths); err != nil {
		return verificationResult{}, fmt.Errorf("check duplicate index paths: %w", err)
	}
	var duplicateIDs int
	if err := runner.QueryRowContext(ctx, "SELECT count(*) FROM (SELECT document_id FROM documents GROUP BY document_id HAVING count(*) > 1)").Scan(&duplicateIDs); err != nil {
		return verificationResult{}, fmt.Errorf("check duplicate document IDs: %w", err)
	}
	if duplicatePaths != 0 || duplicateIDs != 0 {
		return verificationResult{}, fmt.Errorf("index contains duplicate canonical identifiers")
	}
	var ftsRows int
	if err := runner.QueryRowContext(ctx, "SELECT count(*) FROM documents_fts").Scan(&ftsRows); err != nil {
		return verificationResult{}, fmt.Errorf("count FTS rows: %w", err)
	}
	if ftsRows != result.documentCount {
		return verificationResult{}, fmt.Errorf("FTS row count %d does not match document count %d", ftsRows, result.documentCount)
	}
	var joinedRows int
	if err := runner.QueryRowContext(ctx, "SELECT count(*) FROM documents_fts AS f JOIN documents AS d ON d.rowid=f.rowid").Scan(&joinedRows); err != nil {
		return verificationResult{}, fmt.Errorf("check FTS document joins: %w", err)
	}
	if joinedRows != result.documentCount {
		return verificationResult{}, fmt.Errorf("not every FTS row joins to a document")
	}
	if full {
		if _, err := runner.ExecContext(ctx, "INSERT INTO documents_fts(documents_fts, rank) VALUES('integrity-check', 1)"); err != nil {
			return verificationResult{}, fmt.Errorf("check FTS consistency: %w", err)
		}
	}

	var secureDelete int
	if err := runner.QueryRowContext(ctx, "PRAGMA secure_delete").Scan(&secureDelete); err != nil {
		return verificationResult{}, fmt.Errorf("inspect secure_delete: %w", err)
	}
	result.secureDelete = secureDelete == 1
	var ftsSecure string
	err = runner.QueryRowContext(ctx, "SELECT v FROM documents_fts_config WHERE k='secure-delete'").Scan(&ftsSecure)
	if err != nil && err != sql.ErrNoRows {
		return verificationResult{}, fmt.Errorf("inspect FTS5 secure-delete: %w", err)
	}
	if parsed, parseErr := strconv.Atoi(strings.TrimSpace(ftsSecure)); parseErr == nil {
		result.ftsSecureDelete = parsed == 1
	}
	return result, nil
}

func verifyExactTermContents(ctx context.Context, runner sqlRunner) error {
	documentRows, err := runner.QueryContext(ctx, `
SELECT rowid, title, aliases_json, tags_json, kind, body
FROM documents
ORDER BY rowid
`)
	if err != nil {
		return fmt.Errorf("read documents for exact lexical term verification: %w", err)
	}
	expected := map[int64]map[string]struct{}{}
	for documentRows.Next() {
		var rowID int64
		var title, aliasesJSON, tagsJSON, kind, body string
		if err := documentRows.Scan(&rowID, &title, &aliasesJSON, &tagsJSON, &kind, &body); err != nil {
			_ = documentRows.Close()
			return fmt.Errorf("decode document for exact lexical term verification: %w", err)
		}
		var aliases, tags []string
		if err := json.Unmarshal([]byte(aliasesJSON), &aliases); err != nil {
			_ = documentRows.Close()
			return fmt.Errorf("decode aliases for exact lexical term verification: %w", err)
		}
		if err := json.Unmarshal([]byte(tagsJSON), &tags); err != nil {
			_ = documentRows.Close()
			return fmt.Errorf("decode tags for exact lexical term verification: %w", err)
		}
		terms := search.DocumentTokens(title, aliases, tags, kind, []byte(body))
		expected[rowID] = make(map[string]struct{}, len(terms))
		for _, term := range terms {
			expected[rowID][term] = struct{}{}
		}
	}
	if err := documentRows.Err(); err != nil {
		_ = documentRows.Close()
		return fmt.Errorf("read documents for exact lexical term verification: %w", err)
	}
	if err := documentRows.Close(); err != nil {
		return fmt.Errorf("close documents after exact lexical term verification: %w", err)
	}

	termRows, err := runner.QueryContext(ctx, "SELECT document_rowid, term, rune_length FROM document_terms ORDER BY document_rowid, term")
	if err != nil {
		return fmt.Errorf("read exact lexical terms for verification: %w", err)
	}
	for termRows.Next() {
		var rowID int64
		var term string
		var runeLength int
		if err := termRows.Scan(&rowID, &term, &runeLength); err != nil {
			_ = termRows.Close()
			return fmt.Errorf("decode exact lexical term for verification: %w", err)
		}
		terms, exists := expected[rowID]
		if !exists {
			_ = termRows.Close()
			return fmt.Errorf("exact lexical term references an unknown document")
		}
		if _, exists := terms[term]; !exists {
			_ = termRows.Close()
			return fmt.Errorf("index contains an unexpected exact lexical term")
		}
		if runeLength != utf8.RuneCountInString(term) {
			_ = termRows.Close()
			return fmt.Errorf("index contains an incorrect exact lexical term length")
		}
		delete(terms, term)
	}
	if err := termRows.Err(); err != nil {
		_ = termRows.Close()
		return fmt.Errorf("read exact lexical terms for verification: %w", err)
	}
	if err := termRows.Close(); err != nil {
		return fmt.Errorf("close exact lexical terms after verification: %w", err)
	}
	for _, terms := range expected {
		if len(terms) != 0 {
			return fmt.Errorf("index is missing one or more exact lexical terms")
		}
	}
	return nil
}
