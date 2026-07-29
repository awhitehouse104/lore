# Lore v0.3.0 release notes

Lore v0.3.0 adds a disposable, repository-bound SQLite FTS5 index and safe
hybrid lexical search while preserving Markdown and Git as the only canonical
knowledge store.

## Included

- Verified full index builds through private self-contained temporary databases
  and atomic replacement
- Transactional deterministic add/update/delete/no-op reconciliation
- Repository identity binding, Git snapshot freshness, non-Git manifest
  verification, and corruption/incompatibility detection
- `lore index build`, `update`, `status [--verify]`, and idempotent `clear`
- `auto`, `index`, and `filesystem` search backends
- Conservative automatic fallback when FTS cannot preserve lexical behavior
- Quoted FTS term construction with no raw query SQL or FTS syntax
- Exact reuse of the v0.1 public scorer, snippets, line references, filters,
  revisions, and deterministic ties
- Explicit request access policy and repeatable local CLI sensitivity filters
- Best-effort refresh of an existing index after durable capture and
  transaction writes
- WAL concurrency for reads during update and exclusive full-build locking
- Lint warnings for stale/corrupt/incompatible indexes, tracked derived files,
  unsafe symlinks, and open permissions
- Failure-injection, parity, adversarial-query, stale-state, sensitivity,
  locking, and concurrent-read integration coverage

## Compatibility and upgrade

- `lore.yaml` remains schema version `1`.
- Existing v0.2 configurations are valid; omitted index settings receive v0.3
  defaults.
- The new `index` block is optional. Strict v0.2 binaries reject it, so omit it
  during temporary mixed-version operation.
- Search success JSON keeps schema version `1` and existing fields. It adds
  `backend`, `backend_requested`, and `index_state`.
- Existing Markdown requires no migration.
- Existing `.lore/` state remains derived. Run `lore index build` only when an
  index is desired.

The default search backend is `auto`. Without an index, or for a query that
cannot preserve indexed/filesystem parity, v0.3 performs the existing
filesystem search.

## Index storage and recovery

The index and companions live under `.lore/`, are Git-ignored, and use private
permissions where supported. They can contain all locally indexed sensitivity
levels and therefore require the same filesystem protection as canonical
Markdown.

A failed build preserves the prior installed index. A failed incremental update
rolls back its SQL transaction. A failed automatic refresh never rolls back
canonical files, a Git commit, or required push handling.

For operational recovery:

```bash
lore index status --verify
lore index update
# If corrupt or incompatible:
lore index clear
lore index build
```

Never edit the database directly or attempt an in-place schema migration.

## Runtime and dependencies

The new direct dependency is `modernc.org/sqlite v1.54.0` under BSD-3-Clause.
It provides pure-Go SQLite/FTS5; no external SQLite library or CGo is required.
`CGO_ENABLED=0 go build ./cmd/lore` passes.

The full runtime closure and selected module graph are recorded in
[dependencies.md](dependencies.md). `go mod verify` passed and `govulncheck`
v1.6.0 reported no known vulnerabilities on 2026-07-29.

## Benchmark reference

The 10,000-medium-document forced-build benchmark on the release workstation
(Linux/amd64, AMD Ryzen 9 9900X, Go 1.26.5) measured approximately 0.810
seconds, 179,286,976 allocated bytes, 2,221,907 allocations, and a `2.223`
index/text size ratio. See [index.md](index.md) for the exact command and output.

## Security notes

The index is local plaintext derived data, not encryption or an authorization
boundary. All sensitivity levels may be indexed. The CLI's
`--include-sensitivity` option narrows a request policy but does not restrict a
user with filesystem or general shell access.

FTS input is built only from tokenized, quoted terms and remains parameterized.
Path validation rejects traversal and symlink escape. Captured source bodies
remain exact and are never placed in errors.

## Implementation notes and deviations

There are no material deviations from the v0.3 behavioral handoff.

- The representative document schema is extended with deterministic
  `aliases_json`, `tags_json`, and body-line metadata so the existing public
  scorer and snippets can consume exact structured candidates without
  reparsing canonical files.
- Full `index status --verify` performs the write-like FTS5 integrity command.
  Search does not run that command on its read path; explicit non-Git search
  instead performs a read-only canonical manifest comparison so WAL readers
  continue during update.
- Process-shared index operation locking uses Linux `flock`, matching the
  release's supported and fully tested platform.

## Deliberate boundaries

This release does not add embeddings, vector or semantic search, an
authoritative database, automatic synthesis, URL fetching, import pipelines,
page delete/rename, source-body edits, a daemon, watcher, HTTP API, MCP server,
web UI, encryption, secrets management, multi-user authorization, or automatic
transaction-artifact retention.
