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

When a compatible index already exists and `index.auto_refresh_existing` is
enabled, capture attempts an index update only after source write, configured
commit, and push handling complete. Refresh failure is a warning and cannot
undo the source or local Git commit. Capture never creates an index.

## `search`

```text
lore search QUERY... \
  [--scope all|pages|sources] \
  [--kind TOKEN] \
  [--backend auto|index|filesystem] \
  [--include-sensitivity normal|sensitive|local-only ...] \
  [--limit N] \
  [--json]
```

Defaults are `scope=all` and `limit=10`; maximum limit is 100. Query terms split on non-letter/non-number boundaries using Unicode-aware lowercasing.

The explainable scorer favors exact title and alias phrases, metadata tokens, tag phrases, body phrases and bounded token occurrences, then kind tokens. Results with no matched terms are omitted. Ordering is score descending and path ascending. No recency boost or normalization is used.

`auto` is the configuration default. It uses a fresh compatible derived index
only when indexed candidate generation preserves filesystem behavior;
otherwise it falls back with a warning. `index` refuses missing, stale,
corrupt, incompatible, or unsuitable state instead of silently returning old
data. `filesystem` reads Markdown directly.

All local sensitivities are included by default. Repeating
`--include-sensitivity` constructs a narrower explicit request policy, and
filtering occurs before index rows are returned to the core scorer.

Each result contains rank, score, path, URI, ID, title, kind, line range,
bounded snippet, and whole-file SHA-256 revision. Search JSON also contains
`backend`, `backend_requested`, and `index_state`. Oversized documents are
skipped with warnings. Search never mutates canonical knowledge.

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
- optional source `integrated_into` page references;
- uncommitted source changes and detached Git HEAD warnings;
- stale preview warnings and active or malformed recovery-journal state.
- warnings for an existing stale, corrupt, incompatible, busy, or uncertified
  derived index;
- derived-index Git tracking, symlink, and restrictive-permission checks.

An absent index is not a finding. Derived warnings never make otherwise-valid
canonical Markdown invalid. Errors return 1. Warnings return 0 when no errors
exist. Git checks never contact a remote.

## `preview`

```text
lore preview [--input PATH|-] [--json]
```

Input defaults to non-terminal stdin. The strict request object requires
`schema_version`, `message`, and `operations`; unknown fields fail. Requests
are limited to 16 MiB and 50 unique-path operations.

Supported operations are:

- `create_page`: `op`, direct `pages/*.md` path, and complete `content`;
- `update_page`: the same plus the current whole-file `expected_revision`;
- `mark_source_integrated`: source `path`, `expected_revision`, and 1–50 unique
  `page_ids`.

Messages are one line, 1–160 UTF-8 bytes, contain no ASCII controls, and begin
with `integrate:`, `create:`, `update:`, `correct:`, `archive:`, or
`maintenance:`. Git retains the message permanently; do not include raw private
text, medical detail, credentials, or unnecessary sensitive information.

Preview requires a named Git branch and an existing commit. It rejects dirty
target paths, stale revisions, unsafe paths, an active recovery journal, and
page metadata violations. It never mutates the working tree, index, refs, or
history. Instead it overlays the exact effective bytes in memory, runs full
lint, and generates an uncolored unified diff with `a/` and `b/` paths.

Successful previews persist private mode-`0700`/`0600` artifacts beneath
`.lore/transactions/tx_<ULID>/` and return a digest of immutable
`proposal.json`. Lint errors return exit code 1 with the prospective diff and
findings but no committable transaction ID/digest pair.

## `commit`

```text
lore commit TRANSACTION_ID \
  --preview-digest sha256:... \
  [--push | --no-push] \
  [--json]
```

The digest is mandatory and compared in constant time. Commit re-reads and
hash-verifies the proposal, diff, lint, and every resulting-content artifact,
then requires the exact preview branch, HEAD, target existence/revisions, and
clean target status. It reruns prospective lint and regenerates the exact diff
before any write.

Lore flushes an exact-original recovery journal before file application. After
verified atomic publication, it lints the real tree, commits only transaction
paths, and proves that the commit contains every and only those paths with the
proposed bytes. Unrelated staged and unstaged state remains unchanged.

Changed preconditions return exit code 4; there is no force, merge,
ignore-revision, or skip-lint option. Read current state and preview again.
A repeated successful commit returns the original hash with
`already_committed: true`.

`--push`/`--no-push` override `git.auto_push_transactions`. Optional push
failure is a success warning. With `git.require_push: true`, failure returns 3
and states that the canonical commit is safe locally. A successful local commit
is never reset because push or derived-state maintenance fails.

After push handling, commit best-effort updates an already-existing compatible
index when configured. It never creates an index, and refresh failure is only a
warning.

## `transaction list`

```text
lore transaction list \
  [--status previewed|committed|discarded|failed|recovery_required] \
  [--limit N] \
  [--json]
```

The default limit is 20 and maximum is 200. Results are newest transaction ID
first and contain metadata only.

## `transaction show`

```text
lore transaction show TRANSACTION_ID [--diff] [--json]
```

Show verifies available artifacts and returns proposal metadata, lifecycle
state, hashes, and lint summary. `--diff` includes the full exact diff when it
still exists.

## `transaction discard`

```text
lore transaction discard TRANSACTION_ID [--json]
```

Only previewed or failed transactions may be discarded. The operation is
idempotent and blocked by active recovery. It deletes resulting content, diff,
and full lint payloads while retaining proposal/state receipt metadata and a
lint summary. Committed transactions cannot be discarded. v0.3 has no
automatic pruning.

## `recover`

```text
lore recover [--json]
lore recover --rollback [--json]
lore recover --finalize [--json]
```

Without an action, reports the active journal phase and exact recommended
command. `--rollback` is available before a transaction commit: it preflights
all target revisions, restores exact originals or removes Lore-created files,
reruns lint, marks the transaction failed, and removes the journal. It refuses
without changing files when an unexpected edit exists.

`--finalize` modifies no canonical content. It verifies an exact direct child
of the preview base commit on the preview branch, the complete changed-path
set, and every resulting blob SHA-256 before recording the committed state.
Lore never automatically resumes an interrupted apply. See
[recovery.md](recovery.md).

## `index`

```text
lore index build [--force] [--json]
lore index update [--json]
lore index status [--verify] [--json]
lore index clear [--json]
```

`build` requires valid canonical documents and clean managed Git paths. It
scans current Markdown, builds and fully verifies a private temporary database,
then atomically installs it. A current compatible index requires `--force` for
replacement; failed construction preserves the prior index.

`update` requires an existing compatible index and the same canonical/Git
preconditions. It performs deterministic add/update/delete reconciliation and
full verification in one SQL transaction.

`status` is lightweight by default and reports `missing`, `fresh`, `stale`,
`uncertified`, `building`, `corrupt`, or `incompatible`. `--verify` adds full
SQLite/FTS consistency and secure-delete checks, plus canonical manifest
comparison where Git cannot certify freshness. Corrupt and incompatible status
return exit code 3.

`clear` acquires the exclusive derived-index operation lock and removes only
known index, WAL, shared-memory, and temporary-build files. It retains the
repository identity and is idempotent.

See [index.md](index.md) for storage, fallback, locking, recovery, and benchmark
details.

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
  "-X lore/internal/version.Version=0.3.0 \
   -X lore/internal/version.Commit=$(git rev-parse HEAD) \
   -X lore/internal/version.BuildDate=2026-07-29T00:00:00Z" \
  ./cmd/lore
```
