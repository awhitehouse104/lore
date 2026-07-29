package index

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
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
	if schemaVersion != SchemaVersion {
		return verificationResult{}, fmt.Errorf("index schema version %d is incompatible with supported version %d", schemaVersion, SchemaVersion)
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
