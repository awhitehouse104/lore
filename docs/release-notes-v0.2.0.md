# Lore v0.2.0 release notes

Lore v0.2.0 adds reviewable, digest-bound, recoverable page-maintenance
transactions while preserving the v0.1 Markdown, source-capture, search, read,
lint, history, Git-isolation, and JSON schema contracts.

## Included

- Strict bounded `preview` requests with `create_page`, `update_page`, and
  `mark_source_integrated`
- In-memory prospective repository views and complete deterministic lint
- Exact Git no-index unified diffs and SHA-256-bound private artifacts
- Transaction list/show/discard inspection with lifecycle validation and
  tamper detection
- Exact branch, HEAD, revision, clean-target, actor, digest, lint, and diff
  commit preconditions
- Durable exact-original journals and verified atomic file publication
- One exact-path Git commit that preserves unrelated staged and unstaged state
- Commit-tree path/blob verification and idempotent replay
- Optional transaction push with the existing local-commit-survives policy
- Explicit recovery status, no-clobber rollback, and Git-proven finalize
- Optional source `integrated_into` metadata with exact source-body preservation
- Lint findings for missing integrated pages, old previews, and recovery state
- Deterministic injected crash-boundary, rollback, finalize, push, lock, and
  path-isolation coverage

## Compatibility and upgrade

- `lore.yaml` remains schema version `1`.
- CLI success and error JSON remain schema version `1`; existing v0.1 command
  fields and semantics are retained.
- `git.auto_push_transactions` is optional and defaults to `false`.
- Strict v0.1 binaries reject a `lore.yaml` containing that new optional key.
  During mixed-version use, omit it until all clients run v0.2.
- Existing Markdown remains valid. `integrated_into` is optional.
- Existing `.lore/` state is replaceable; v0.2 creates `transactions/` and
  `recovery/` lazily.

## Runtime and dependencies

The Lore binary and Git are the only runtime prerequisites. Network access
occurs only for an explicitly configured or requested Git push.

The dependency set is unchanged:

| Module | Version | License | Purpose |
|---|---|---|---|
| Go standard library/toolchain | Go 1.26 | BSD-style Go license | CLI, filesystem, JSON, Git process adapter, hashing |
| `github.com/oklog/ulid/v2` | `v2.1.2` | Apache-2.0 | cryptographic source and transaction ULIDs |
| `go.yaml.in/yaml/v4` | `v4.0.0-rc.6` | Apache-2.0 | YAML frontmatter and strict configuration |
| `github.com/pborman/getopt` | `v0.0.0-20170112200414-7148bc3a4c30` | BSD-3-Clause | transitive module-graph entry of YAML v4; not needed by Lore's package graph |

No database, LLM SDK, diff library, server framework, daemon, MCP package, or
HTTP dependency was added. Unified diffs use a narrowly wrapped `git diff
--no-index` argument-array invocation.

## Security and recovery notes

Transaction and recovery artifacts can contain complete private synthesized
documents and exact originals. They use private permissions where supported
and remain derived state, but v0.2 does not automatically prune committed
transactions. Commit messages are retained in Git and must not contain
unnecessary sensitive text.

An active recovery journal blocks Lore writers. Run `lore recover` and follow
its recommended rollback or finalize action. Generated agent instructions are
cooperative policy; they do not prevent a general shell process running under
the same account from bypassing Lore.

## Deliberate boundaries

This release does not add page delete/rename, arbitrary source metadata edits,
source-body edits, automatic synthesis, conflict merging, semantic/vector
search, a database, daemon, watcher, importer, MCP/HTTP server, web UI,
encryption, secret storage, multi-user authorization, or automatic artifact
retention.

## Implementation deviations

- Atomic no-clobber publication for newly created files uses the same
  same-directory hard-link technique documented in v0.1 rather than a replacing
  rename. Updates use flushed same-directory temporary files and rename.
- Git is used for unified-diff generation instead of adding a Go diff
  dependency.
- Discarded transactions retain immutable proposal metadata and mutable state
  with a lint summary; full content, diff, and lint payload artifacts are
  removed.
