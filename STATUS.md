# Lore implementation status

## Current milestone

Milestone 5 — hardening and v0.1 release readiness (complete).

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
- Hardened JSON error handling, option/path safety, cancellation, deterministic output ordering, and runtime symlink checks.
- Completed the README, CLI/data-model/security guides, build metadata injection, and v0.1.0 release notes.

## Commands implemented

- `lore version`
- `lore init`
- `lore lint`
- `lore capture`
- `lore read`
- `lore search`
- `lore recent`

## Checks passing

- `go test ./...`
- `go vet ./...`
- `go build ./cmd/lore`
- `go test -race ./...`
- `make check`
- `make test-race`
- End-to-end CLI session covering init, capture, search, read, lint, recent, and version

## Known issues

- No known v0.1 correctness issues. Deliberate product limitations are documented in `README.md` and the release notes.

## Material deviations from spec

- Atomic source publication uses a same-directory hard-link/no-clobber publish followed by temporary-link removal instead of `rename`. This preserves atomic visibility and guarantees that an existing destination cannot be overwritten using the Go standard library on the primary Linux target.

## Next milestone

The v0.1 definition of done is complete. Planned v0.2 safe synthesized-write transactions have not been started.
