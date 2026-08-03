# Lore data model

Lore stores canonical knowledge as UTF-8 Markdown with YAML frontmatter.
Runtime state, the local SQLite search index, and clients are derived adapters.

## Repository layout

```text
lore-home/
├── README.md
├── AGENTS.md
├── CLAUDE.md
├── lore.yaml
├── .gitignore
├── pages/
├── sources/
├── assets/
├── system/
│   ├── OPERATING_RULES.md
│   ├── PAGE_TEMPLATE.md
│   └── SOURCE_TEMPLATE.md
└── .lore/
```

`pages/**` and `sources/**` are managed content roots. Capture can create only
date-partitioned source files. Lore transactions can create or update only
direct `pages/*.md` children and can update only `integrated_at` and
`integrated_into` in an existing source. `system/**`, repository instructions,
configuration, Git metadata, and `.lore/**` are protected from normal content
writes.

Pages remain flat in v0.4. Sources are partitioned by their UTC capture date:

```text
sources/YYYY/MM/src_<26-character-uppercase-ULID>-<kind>.md
```

## Frontmatter and exact bodies

Managed Markdown starts with a line containing exactly `---`, ends frontmatter at the next exact `---` line, and starts the body immediately after the closing delimiter's newline.

Lore does not trim or normalize body bytes while parsing. A body's trailing-newline state, CRLF bytes, Unicode encoding, and empty/non-empty state are significant.

Unknown frontmatter fields are permitted by lint for forward compatibility. Required schema-version-1 fields and their types remain enforced.

## Source documents

```markdown
---
id: src_01ARZ3NDEKTSV4RRFFQ69G5FAV
kind: user_statement
captured_at: "2026-07-22T16:30:21.123456789Z"
origin: codex
origin_ref: optional-client-reference
raw_sha256: sha256:4d7a0f...
sensitivity: normal
tags:
  - project-foo
integrated_at: "2026-07-28T20:10:00Z"
integrated_into:
  - page_project_foo
---
Exact captured bytes begin here.
```

Required fields:

- `id`: `src_` plus a canonical uppercase ULID
- `kind`: token matching `^[a-z][a-z0-9_-]*$`
- `captured_at`: RFC 3339 UTC timestamp
- `origin`: token using the same validation as `kind`
- `raw_sha256`: lowercase SHA-256 of the exact body, prefixed by `sha256:`
- `sensitivity`: `normal`, `sensitive`, or `local-only`

Optional fields are `origin_ref`, `tags`, `integrated_at`, and
`integrated_into`. Tags must be non-empty strings. `integrated_at`, when
present, is RFC 3339 UTC. `integrated_into` contains unique valid page IDs.
Lore writes it as a sorted union and lint warns, rather than errors, when a
referenced page no longer exists.

The source filename ID and kind must equal its frontmatter, and its path year/month must equal `captured_at` in UTC. Source bodies are append-oriented and immutable by policy. Lore does not deduplicate identical captures.

## Synthesized pages

```markdown
---
id: page_project_foo
title: Project Foo
kind: project
aliases:
  - foo
created: "2026-07-22"
updated: "2026-07-22"
status: active
sensitivity: normal
tags:
  - deployment
---
# Summary

Project Foo is ...

## Sources

- [Deployment constraint](../sources/2026/07/src_01ARZ3NDEKTSV4RRFFQ69G5FAV-user_statement.md)
```

Required fields:

- `id`: stable identifier matching `^page_[a-z0-9][a-z0-9_]*$`
- `title`: non-empty title
- `kind`: token matching `^[a-z][a-z0-9_-]*$`
- `created` and `updated`: ISO `YYYY-MM-DD`, with `updated` not before `created`
- `status`: `active`, `inactive`, `archived`, or `superseded`
- `sensitivity`: source sensitivity enum

Optional `aliases` and `tags` are lists of non-empty strings. Page titles and aliases may not identify more than one page case-insensitively.

Transaction page creation and update always use a complete proposed document;
Lore does not merge. Page `id` and `created` are immutable during update.
`updated` cannot regress, and a change outside that field requires an
`updated` date at least as recent as the current UTC calendar date. If a client
is still on the preceding local calendar date, the MCP `updated_too_old` error
reports Lore's required UTC `minimum` date.

## Modeling conventions

Source `kind` is an open validated token, so callers may use values such as
`approval`, `decision`, or `conversation_excerpt` without a schema change. A
context-dependent fragment such as "let's do it" should not be captured as if
it were self-explanatory: preserve a minimally sufficient verbatim exchange and
an `origin_ref` when available, while keeping any inferred interpretation out
of the immutable source wording.

Store a durable fact once on the narrowest page that naturally owns it. A fact
shared by several people or projects normally belongs on a shared subject,
household, event, or dated-plan page; entity profiles should link to that page
instead of copying its complete details. These are curation conventions rather
than new authoritative relationship fields. Ordinary relative Markdown links
remain lint-checked and searchable.

When a source uses a relative temporal expression, resolve it into an explicit
date or range only when capture time and context make the intended period
clear. Preserve the original wording, identify the resolution as inference,
and preserve ambiguity or ask for clarification when multiple interpretations
remain plausible.

Human-facing dates, deadlines, and relative expressions use the known user
timezone unless the source or request specifies another one. Preserve explicit
source timezones, and ask when the user timezone is unknown and materially
changes the interpretation. This semantic clock is separate from Lore's UTC
metadata clock: for example, a user's late evening can already be the next UTC
date required by page `updated` validation.

## Links, references, and revisions

Relative inline Markdown links and reference definitions are checked from the
containing document. Repository escapes and missing targets are lint errors.
External schemes and pure anchors are not resolved; anchors within local files
are not validated in v0.4.

Read references resolve in this priority:

1. exact relative path under `pages/` or `sources/`
2. exact document ID
3. exact filename stem
4. exact case-insensitive page title
5. exact case-insensitive page alias

Every read and search result includes a provider-neutral `revision` equal to `sha256:` plus the digest of the complete current file bytes. Lore URIs use repository-relative paths and optional line fragments:

```text
lore://pages/project-foo.md#L12-L14
```

## Configuration and runtime state

`lore.yaml` schema version 1 has fixed content paths and strict known fields. The capture maximum defaults to 4 MiB and cannot exceed 64 MiB.

Optional Git configuration fields are:

- `auto_commit_captures` (default `true`);
- `auto_push_captures` (default `false`);
- `auto_push_transactions` (default `false`);
- `remote` (default `origin`);
- `require_push` (default `false`).

Optional index configuration fields are:

- `backend` (`auto`, `index`, or `filesystem`; default `auto`);
- `auto_refresh_existing` (default `true`);
- `candidate_multiplier` (default `20`);
- `minimum_candidates` (default `200`);
- `maximum_candidates` (default `2000`).

See [configuration.md](configuration.md) for exact bounds and compatibility.

`.lore/` contains replaceable runtime state. It must be ignored by Git and is
never canonical knowledge:

```text
.lore/
├── write.lock
├── index.sqlite
├── index.sqlite-wal
├── index.sqlite-shm
├── index.operation.lock
├── repository-id
├── transactions/
│   └── tx_<ULID>/
│       ├── proposal.json
│       ├── state.json
│       ├── retention.json
│       ├── diff.patch
│       ├── lint.json
│       └── content/
└── recovery/
    └── active/
        ├── journal.json
        └── originals/
```

Transaction proposals are immutable typed JSON with a trailing newline. The
preview digest is SHA-256 over those exact bytes. The proposal records hashes
for the diff, lint report, and each exact resulting document. Lifecycle states
are `previewed`, `applying`, `committed`, `discarded`, `failed`, and
`recovery_required`; invalid transitions are integrity errors.

`retention.json` is optional and exists only for a committed transaction whose
payload compaction has started. It binds the transaction ID and preview digest
to a sorted manifest containing the path, SHA-256, and logical byte count of
every removable payload. Phase `pruning` permits any prefix of those verified
artifacts to be absent after interruption; phase `pruned` requires all of them
to be absent. Proposal and state receipts remain.

An active recovery journal contains exact originals or explicit absence
markers and advances through `prepared`, `applying_files`, `files_applied`,
`git_committed`, and `finalized`. It is flushed before canonical mutation and
blocks all other writers. See [recovery.md](recovery.md).

Derived transaction artifacts can contain synthesized page bytes and diffs.
They use private permissions where supported. Explicit transaction pruning can
compact old committed payloads, but there is no automatic retention policy.

`write.lock` is a persistent private regular file used as the target of the
repository-wide Linux `flock`. Its JSON metadata is diagnostic rather than
authoritative and contains schema version, PID, hostname, command, and start
time. Unlocking does not delete the file; descriptor closure on normal exit or
process death releases the kernel lock.

The SQLite index contains normalized document metadata, body text, exact
canonical revisions, snapshot metadata, an external-content FTS5 table, and a
derived exact-term table used for corpus rarity statistics and fuzzy candidate
lookup. Each term records its exact Unicode rune length; `kind` participates in
FTS candidate generation. The current index application schema is version `3`,
independent of repository/JSON schema version `1`. It is never edited or
migrated in place; incompatibility requires replacement. Installed databases
use WAL, while full builds use one self-contained temporary database so only a
verified artifact is promoted.

All sensitivity levels may be indexed locally. Access policy is applied before
candidate rows leave the index package. The index, companions, operation lock,
and repository identity are private derived files and must remain ignored by
Git. See [index.md](index.md).
