# Lore v0.1.0 release notes

Lore v0.1.0 establishes the durable Markdown-and-Git knowledge contract and deterministic CLI surface.

## Included

- Idempotent knowledge-repository initialization with embedded operating rules and templates
- Exact-byte source capture with cryptographic ULIDs and SHA-256 integrity metadata
- Advisory write locking, atomic no-clobber publication, source-only Git commits, and explicit push policy
- Priority-based document reads with line ranges, ambiguity handling, Lore URIs, and revisions
- Deterministic Unicode-aware lexical evidence search
- Repository, metadata, source-integrity, relative-link, and selected Git-state lint checks
- Content-filtered and complete recent Git history
- Stable schema-version-1 JSON success and error responses
- Hermetic unit and Git integration coverage, including race tests

## Runtime requirements

The Lore binary and Git are the only runtime prerequisites. Network access is used only for an explicitly configured capture push.

## Deliberate boundaries

This release has no LLM call, generated answer, page-write transaction, database, semantic search, daemon, MCP/HTTP server, web UI, importer, encryption, secret store, or multi-user permission system.

`local-only` is metadata only. Purging sensitive data requires Git-history and backup handling outside ordinary Lore operations.

## Upgrade notes

This is the first release. The on-disk configuration version and CLI JSON schema version are both `1`.
