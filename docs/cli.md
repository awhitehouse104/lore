# Lore CLI reference

## General behavior

```text
lore [--repo PATH] [--json] <command> [options]
```

Repository resolution uses `--repo`, then `LORE_REPO`, then an upward search for `lore.yaml`. `init` is the exception and uses its positional path or the current directory.

Commands are non-interactive. Normal results go to stdout, while diagnostics, warnings, and the identifying header for human-mode `read` go to stderr. JSON mode emits one undecorated object on stdout. Every JSON response contains `schema_version: 1`, uses `snake_case` keys, and is deterministic for the same repository state.

JSON errors have this shape:

```json
{
  "schema_version": 1,
  "error": {
    "code": "ambiguous_reference",
    "message": "reference matched more than one document",
    "details": {
      "candidates": []
    }
  }
}
```

Exit codes are stable:

| Code | Meaning |
|---:|---|
| 0 | success |
| 1 | validation or lint findings |
| 2 | usage or configuration error |
| 3 | filesystem, Git, or integrity operation failure |
| 4 | conflict, ambiguity, or lock contention |

All commands support `--help`.

## `init`

```text
lore init [PATH] [--no-git] [--json]
```

Creates missing directories and baseline files without overwriting existing files. It is idempotent for an initialized repository. Unless `--no-git` is used, it creates a `main` Git repository when needed. If Git identity is available, a new repository receives:

```text
init: create Lore knowledge repository
```

Missing Git identity is a successful initialization with an actionable warning.

## `capture`

```text
lore capture \
  --kind TOKEN \
  --origin TOKEN \
  [--origin-ref STRING] \
  [--sensitivity normal|sensitive|local-only] \
  [--tag STRING ...] \
  [--text STRING | --file PATH] \
  [--allow-empty] \
  [--no-commit] \
  [--push | --no-push] \
  [--json]
```

When neither `--text` nor `--file` is present, capture reads non-terminal stdin. Input is bounded by `capture.max_bytes` and must be valid UTF-8. Prefer stdin to `--text` for private material.

Capture validates metadata before acquiring `.lore/write.lock`, then publishes one no-clobber source file. The lock records PID, hostname, command, and start time. Lore never automatically removes an old lock; contention reports its metadata and manual recovery path.

Auto-commit uses a path-limited Git commit:

```text
capture: <kind> <source-id>
```

Unrelated staged and working-tree changes remain untouched. `--no-commit` disables commit and push. Push overrides select whether to push the current branch to the configured remote.

The source remains present after every later Git failure. A required push failure reports that its commit is safe locally.

## `search`

```text
lore search QUERY... \
  [--scope all|pages|sources] \
  [--kind TOKEN] \
  [--limit N] \
  [--json]
```

Defaults are `scope=all` and `limit=10`; maximum limit is 100. Query terms split on non-letter/non-number boundaries using Unicode-aware lowercasing.

The explainable scorer favors exact title and alias phrases, metadata tokens, tag phrases, body phrases and bounded token occurrences, then kind tokens. Results with no matched terms are omitted. Ordering is score descending and path ascending. No recency boost or normalization is used.

Each result contains rank, score, path, URI, ID, title, kind, line range, bounded snippet, and whole-file SHA-256 revision. Oversized documents are skipped with warnings. Search never mutates the repository.

## `read`

```text
lore read REFERENCE [--lines START:END] [--json]
```

Reference priority is exact path, ID, stem, page title, then page alias. A rule with multiple matches returns exit code 4 and sorted candidates. Absolute paths, `..` traversal, and symlink escape are rejected.

Line numbers are one-indexed and inclusive. An end beyond EOF is clamped; malformed, reversed, non-positive, or start-beyond-EOF ranges are rejected. Without a range, content is returned exactly.

## `lint`

```text
lore lint [--json]
```

Lint checks:

- `lore.yaml` presence and strict schema;
- required directories and Git ignore state;
- managed regular-file, symlink, size, and UTF-8 rules;
- frontmatter presence, types, tokens, dates, enums, and IDs;
- globally duplicate IDs and ambiguous page titles/aliases;
- exact source body hashes, source filename metadata, and UTC date partitions;
- inline and reference-style relative Markdown link existence and repository containment;
- uncommitted source changes and detached Git HEAD warnings.

Errors return 1. Warnings return 0 when no errors exist. Git checks never contact a remote.

## `recent`

```text
lore recent [--limit N] [--all] [--json]
```

The default limit is 20 and maximum is 200. A Git repository is required. Default history is path-limited to `pages/` and `sources/`; `--all` removes the path filter. JSON contains full commit hashes, UTC commit timestamps, author fields, and subjects.

## `version`

```text
lore version [--json]
```

Human output:

```text
lore <version> (<commit>, <build-date>)
```

Build variables are injectable:

```bash
go build -ldflags \
  "-X lore/internal/version.Version=0.1.0 \
   -X lore/internal/version.Commit=$(git rev-parse HEAD) \
   -X lore/internal/version.BuildDate=2026-07-27T00:00:00Z" \
  ./cmd/lore
```
