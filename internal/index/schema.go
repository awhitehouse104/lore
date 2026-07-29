package index

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

const schemaSQL = `
CREATE TABLE metadata (
    key   TEXT PRIMARY KEY NOT NULL,
    value TEXT NOT NULL
);

CREATE TABLE documents (
    rowid           INTEGER PRIMARY KEY,
    path            TEXT NOT NULL UNIQUE,
    document_id     TEXT NOT NULL UNIQUE,
    document_type   TEXT NOT NULL CHECK (document_type IN ('page', 'source')),
    title           TEXT NOT NULL,
    kind            TEXT NOT NULL,
    sensitivity     TEXT NOT NULL,
    aliases_text    TEXT NOT NULL,
    tags_text       TEXT NOT NULL,
    body            TEXT NOT NULL,
    body_line_start INTEGER NOT NULL CHECK (body_line_start > 0),
    revision        TEXT NOT NULL,
    content_sha256  TEXT NOT NULL,
    created_at      TEXT,
    updated_at      TEXT,
    indexed_at      TEXT NOT NULL
);

CREATE INDEX documents_document_id_idx ON documents(document_id);
CREATE INDEX documents_type_idx ON documents(document_type);
CREATE INDEX documents_sensitivity_idx ON documents(sensitivity);
CREATE INDEX documents_revision_idx ON documents(revision);

CREATE VIRTUAL TABLE documents_fts USING fts5(
    title,
    aliases_text,
    tags_text,
    path,
    body,
    content='documents',
    content_rowid='rowid',
    tokenize='unicode61 remove_diacritics 2',
    prefix='2 3 4'
);
`

var requiredMetadataKeys = []string{
	"index_schema_version",
	"lore_version",
	"repository_identity",
	"indexed_head",
	"indexed_branch",
	"indexed_at",
	"index_build_id",
	"fts5_version",
	"build_complete",
}

func createSchema(ctx context.Context, transaction *sql.Tx) error {
	if _, err := transaction.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("create index schema: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, "INSERT INTO documents_fts(documents_fts, rank) VALUES('secure-delete', 1)"); err != nil {
		return fmt.Errorf("enable FTS5 secure deletion: %w", err)
	}
	return nil
}

func writeMetadata(ctx context.Context, transaction *sql.Tx, values map[string]string) error {
	statement, err := transaction.PrepareContext(ctx, "INSERT INTO metadata(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value")
	if err != nil {
		return fmt.Errorf("prepare index metadata: %w", err)
	}
	defer statement.Close()
	for _, key := range requiredMetadataKeys {
		value, exists := values[key]
		if !exists {
			return fmt.Errorf("index metadata value %s is missing", key)
		}
		if _, err := statement.ExecContext(ctx, key, value); err != nil {
			return fmt.Errorf("write index metadata %s: %w", key, err)
		}
	}
	return nil
}

func readMetadata(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (map[string]string, error) {
	rows, err := queryer.QueryContext(ctx, "SELECT key, value FROM metadata ORDER BY key")
	if err != nil {
		return nil, fmt.Errorf("read index metadata: %w", err)
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("decode index metadata: %w", err)
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read index metadata: %w", err)
	}
	for _, key := range requiredMetadataKeys {
		if _, exists := values[key]; !exists {
			return nil, fmt.Errorf("required index metadata %s is missing", key)
		}
	}
	return values, nil
}

func metadataSchemaVersion(values map[string]string) (int, error) {
	value, err := strconv.Atoi(values["index_schema_version"])
	if err != nil {
		return 0, fmt.Errorf("index schema version is malformed")
	}
	return value, nil
}
