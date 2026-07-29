package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	_ "modernc.org/sqlite"
)

var ErrFTS5Unsupported = errors.New("linked SQLite build does not support FTS5")

type openMode string

const (
	openReadOnly  openMode = "ro"
	openReadWrite openMode = "rw"
	openCreate    openMode = "rwc"
)

func openDatabase(ctx context.Context, path string, mode openMode, journalMode string) (*sql.DB, string, error) {
	location := &url.URL{Scheme: "file", Path: path}
	query := location.Query()
	query.Set("mode", string(mode))
	location.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", location.String())
	if err != nil {
		return nil, "", fmt.Errorf("open index database: %w", err)
	}
	closeOnError := func(operation string, cause error) (*sql.DB, string, error) {
		_ = db.Close()
		return nil, "", fmt.Errorf("%s: %w", operation, cause)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	if err := db.PingContext(ctx); err != nil {
		return closeOnError("connect to index database", err)
	}
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA secure_delete = ON",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			return closeOnError("configure index database", err)
		}
	}
	if journalMode != "" {
		var actual string
		if err := db.QueryRowContext(ctx, "PRAGMA journal_mode = "+journalMode).Scan(&actual); err != nil {
			return closeOnError("configure index journal mode", err)
		}
		if !strings.EqualFold(actual, journalMode) {
			return closeOnError("configure index journal mode", fmt.Errorf("SQLite retained journal mode %s", actual))
		}
	}
	if err := probeFTS5(ctx, db); err != nil {
		return closeOnError("probe FTS5 capability", err)
	}
	var sqliteVersion string
	if err := db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&sqliteVersion); err != nil {
		return closeOnError("read SQLite version", err)
	}
	return db, sqliteVersion, nil
}

func probeFTS5(ctx context.Context, db *sql.DB) error {
	connection, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("%w: open capability connection", ErrFTS5Unsupported)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "DROP TABLE IF EXISTS temp.lore_fts5_probe"); err != nil {
		return fmt.Errorf("%w: prepare capability probe", ErrFTS5Unsupported)
	}
	if _, err := connection.ExecContext(ctx, "CREATE VIRTUAL TABLE temp.lore_fts5_probe USING fts5(value)"); err != nil {
		return fmt.Errorf("%w", ErrFTS5Unsupported)
	}
	defer connection.ExecContext(context.Background(), "DROP TABLE IF EXISTS temp.lore_fts5_probe")
	if _, err := connection.ExecContext(ctx, "INSERT INTO temp.lore_fts5_probe(value) VALUES ('capability')"); err != nil {
		return fmt.Errorf("%w: write capability probe", ErrFTS5Unsupported)
	}
	var count int
	if err := connection.QueryRowContext(ctx, "SELECT count(*) FROM temp.lore_fts5_probe WHERE lore_fts5_probe MATCH 'capability'").Scan(&count); err != nil || count != 1 {
		return fmt.Errorf("%w: query capability probe", ErrFTS5Unsupported)
	}
	return nil
}
