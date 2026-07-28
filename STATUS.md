# Lore implementation status

## Current release and milestone

Lore v0.2.0 — implementation complete and release-verified.

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
- v0.2 M3 `make check`
- v0.2 M3 `make test-race`
- v0.2 M4 `make check`
- v0.2 M4 `make test-race`
- v0.2 M5 `make check`
- v0.2 M5 `make test-race`
- v0.2 M5 `go mod tidy -diff`: clean
- v0.2 M5 `go mod verify`: passed (`all modules verified`)
- v0.2 M5 `govulncheck ./...`: passed (`No vulnerabilities found`)
- Release build/version injection and generated-template initialization/lint smoke: passed

## v0.2 completed milestones

- M1: strict bounded transaction requests, operation/message/path validation, deterministic proposal and state contracts, private atomic artifact storage with tamper detection, source integration metadata merging with exact body preservation, optional `integrated_into` linting, and real/overlay repository views for prospective full-tree lint.
- M2: `preview` with exact snapshot/revision/dirty-target checks, immutable page rules, in-memory prospective lint, Git no-index unified diffs, atomic proposal persistence, and verified `transaction list`, `show`, and idempotent `discard` commands.
- M3: digest-bound `commit`, durable exact-original recovery journals, verified atomic file application, full real-tree lint, exact-path Git commits, commit-tree verification, rollback on pre-commit failure, idempotent success, push policy, and preservation of unrelated staged and unstaged changes.
- M4: `recover` status/rollback/finalize, exact-revision rollback preflight, direct-child Git/blob proof for finalize, write blocking while recovery is active, deterministic injected interruption hooks, no-clobber external-edit handling, and lint findings for active/malformed recovery and stale previews.
- M5: v0.2 versioning, generated agent rules, README/CLI/data/security/recovery documentation, upgrade and release notes, dependency/license inventory, path/argument/error-leak hardening, discard receipt cleanup, and the complete release audit.

Milestone commits:

- M1: `b8f1965`
- M2: `cb8f5c3`
- M3: `6854787`
- M4: `708bc23`
- M5: the release commit tagged `v0.2.0`

## Known issues

- No known v0.2 correctness issues.
- v0.2 has no automatic transaction-artifact retention or pruning.
- The supported and fully tested target is Linux with Git available.

## Material deviations and compatibility notes

- New-file publication uses a same-directory hard-link/no-clobber publish followed by temporary-link removal instead of a replacing rename. Updates use flushed same-directory temporary files and rename.
- Unified diffs use a narrowly wrapped `git diff --no-index` operation rather than a new Go dependency.
- Discarded transactions retain proposal/state receipt metadata and a lint summary; full content, diff, and lint payload artifacts are removed.
- Knowledge-repository `lore.yaml` remains at `version: 1`. `git.auto_push_transactions` is optional and defaults to `false`; strict v0.1 binaries reject a configuration that includes the new key.

## Next checkpoint

Publish the verified `v0.2.0` commit and annotated tag when desired, then scope
the next release without changing the v0.2 transaction and compatibility
contracts.
