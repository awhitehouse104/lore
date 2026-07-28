# Lore implementation status

## Current release and milestone

Lore v0.2 — Milestone 3: transactional commit and Git isolation.

## Verified baseline

- v0.1 final commit: `69e250698694c8489ffd4b967fe98839dbf81ecb`
- v0.1 release tag: `v0.1.0`
- Baseline date: 2026-07-28
- `make check`: passed
- `make test-race`: passed
- `go mod verify`: passed (`all modules verified`)
- Working tree before v0.2 changes: clean

The first baseline attempt ran with a fresh empty `/tmp` module cache and failed only because sandboxed network access could not download the two versions already locked in `go.mod`. After downloading those exact modules through the approved network path, the complete baseline rerun passed.

## v0.2 milestone plan

1. **Transaction contracts and overlay view**
   - Strict request/domain validation, actor seam, safe page paths, overlay repository reads, prospective lint, deterministic proposal persistence.
2. **Preview and inspection**
   - Effective operations, source integration metadata, unified diffs and artifact hashes, `preview`, and transaction list/show/discard commands.
3. **Commit and Git isolation**
   - Base/revision/dirty-path preconditions, durable apply journal, multi-path commit verification, push policy, and commit idempotency.
4. **Recovery and fault injection**
   - Recovery status, rollback/finalize, phase reconciliation, no-clobber behavior, and injected crash-boundary integration coverage.
5. **Documentation and v0.2 release**
   - Generated agent rules, README/CLI/data/security/recovery docs, config migration notes, release notes, dependency/license/security audit, complete checks, and `v0.2.0` tag.

## v0.1 completed capabilities

- `lore init`, `capture`, `search`, `read`, `lint`, `recent`, and `version`
- Strict v1 configuration and embedded knowledge-repository templates
- Exact-byte source capture, SHA-256 integrity, advisory locking, isolated Git commits, and explicit push policy
- Priority-based reads, deterministic lexical evidence search, repository/link/source-integrity lint, and Git history
- Stable CLI JSON schema version 1 and transport-independent core operations

## Checks passing

- v0.1 baseline checks listed above
- v0.2 M1 `make check`
- v0.2 M2 `make check`

## v0.2 completed milestones

- M1: strict bounded transaction requests, operation/message/path validation, deterministic proposal and state contracts, private atomic artifact storage with tamper detection, source integration metadata merging with exact body preservation, optional `integrated_into` linting, and real/overlay repository views for prospective full-tree lint.
- M2: `preview` with exact snapshot/revision/dirty-target checks, immutable page rules, in-memory prospective lint, Git no-index unified diffs, atomic proposal persistence, and verified `transaction list`, `show`, and idempotent `discard` commands.

## Known issues

- No known v0.1 correctness issues.
- `commit` and `recover` are not implemented yet.

## Material deviations and compatibility notes

- v0.1 atomic source publication uses a same-directory hard-link/no-clobber publish followed by temporary-link removal instead of `rename`. This preserves atomic visibility and guarantees that an existing destination cannot be overwritten on the primary Linux target.
- Knowledge-repository `lore.yaml` remains at `version: 1`. v0.2 will add optional configuration fields; older strict v0.1 binaries may reject repositories that use them.

## Next checkpoint

Implement and commit v0.2 Milestone 3 transactional apply and exact-path Git commit behavior.
