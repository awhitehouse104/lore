# Lore data model

Lore stores canonical knowledge as UTF-8 Markdown with YAML frontmatter. Runtime state, search implementations, clients, and future indexes are derived adapters.

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

`pages/**` and `sources/**` are managed content roots. Normal v0.1 content operations can write only new files beneath `sources/`. `system/**`, repository instructions, configuration, Git metadata, and `.lore/**` are protected from normal content writes.

Pages remain flat in v0.1. Sources are partitioned by their UTC capture date:

```text
sources/YYYY/MM/src_<26-character-uppercase-ULID>-<kind>.md
```

## Frontmatter and exact bodies

Managed Markdown starts with a line containing exactly `---`, ends frontmatter at the next exact `---` line, and starts the body immediately after the closing delimiter's newline.

Lore does not trim or normalize body bytes while parsing. A body's trailing-newline state, CRLF bytes, Unicode encoding, and empty/non-empty state are significant.

Unknown frontmatter fields are permitted by lint for forward compatibility. Required v0.1 fields and their types remain enforced.

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

Optional fields are `origin_ref`, `tags`, and `integrated_at`. Tags must be non-empty strings. `integrated_at`, when present, is RFC 3339.

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
- `kind`: v0.1 token
- `created` and `updated`: ISO `YYYY-MM-DD`, with `updated` not before `created`
- `status`: `active`, `inactive`, `archived`, or `superseded`
- `sensitivity`: source sensitivity enum

Optional `aliases` and `tags` are lists of non-empty strings. Page titles and aliases may not identify more than one page case-insensitively.

Lore v0.1 reads, searches, and lints pages but never creates or updates them.

## Links, references, and revisions

Relative inline Markdown links and reference definitions are checked from the containing document. Repository escapes and missing targets are lint errors. External schemes and pure anchors are not resolved; anchors within local files are not validated in v0.1.

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

`.lore/` contains replaceable runtime state such as the advisory write lock. It must be ignored by Git and is never canonical knowledge.
