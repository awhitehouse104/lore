# Lore implementation status

## Current milestone

Milestone 2 — capture and Git durability (complete).

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

## Commands implemented

- `lore version`
- `lore init`
- `lore lint` (all non-link structural and document-integrity checks; Git/link hardening remains in Milestone 4)
- `lore capture`

## Checks passing

- `go test ./...`
- `go vet ./...`
- `go build ./cmd/lore`
- `go test -race ./...`

## Known issues

- `search`, `read`, and `recent` are not implemented yet.
- Lint link validation and optional Git-state warnings remain for Milestone 4.

## Material deviations from spec

- Atomic source publication uses a same-directory hard-link/no-clobber publish followed by temporary-link removal instead of `rename`. This preserves atomic visibility and guarantees that an existing destination cannot be overwritten using the Go standard library on the primary Linux target.

## Next milestone

Milestone 3 — read and search.
