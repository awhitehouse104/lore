# Lore implementation status

## Current milestone

Milestone 4 — lint and recent history (complete).

## Completed work

- Initialized the application repository and Go module.
- Added project development instructions and the initial package boundaries.
- Added shared API error/result types, version metadata, command dispatch, and build targets.
- Added strict v0.1 configuration parsing with defaults and unknown-key rejection.
- Added centralized safe-path/root resolution and symlink-escape rejection.
- Added byte-preserving Markdown frontmatter parsing, document validation, and SHA-256 helpers.
- Added embedded knowledge-repository templates and idempotent Git-aware initialization.
- Added structural, metadata, duplicate-ID, alias-collision, and source-integrity lint checks.
- Added bounded UTF-8 input, cryptographic source ULIDs, deterministic source serialization, and exact-body hashing.
- Added inspectable advisory write locking and atomic no-clobber source publication.
- Added path-limited capture commits, explicit branch pushes, push policy handling, and partial-failure recovery details.
- Added a shared managed-document catalog with priority-based reference resolution and ambiguity diagnostics.
- Added exact, one-indexed line reads with clamping, Lore URIs, and whole-file revision hashes.
- Added deterministic Unicode-aware lexical search with explainable scoring, bounded snippets, scopes, kind filters, and stable tie-breaking.
- Completed lint with configuration, relative-link, repository-escape, Git dirty-source, and detached-HEAD checks.
- Added deterministic NUL-delimited Git history parsing with content-only and all-commit modes.

## Commands implemented

- `lore version`
- `lore init`
- `lore lint` (all non-link structural and document-integrity checks; Git/link hardening remains in Milestone 4)
- `lore capture`
- `lore read`
- `lore search`
- `lore recent`

## Checks passing

- `go test ./...`
- `go vet ./...`
- `go build ./cmd/lore`
- `go test -race ./...`

## Known issues

- User documentation and release hardening remain for Milestone 5.

## Material deviations from spec

- Atomic source publication uses a same-directory hard-link/no-clobber publish followed by temporary-link removal instead of `rename`. This preserves atomic visibility and guarantees that an existing destination cannot be overwritten using the Go standard library on the primary Linux target.

## Next milestone

Milestone 5 — hardening and v0.1 release readiness.
