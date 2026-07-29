# Lore implementation status

## Current release and milestone

Lore v0.4.0 — Milestones 1–2 complete; Milestone 3 (stateless Streamable HTTP)
is next.

## v0.4 completed milestones

- **M1 — SDK adapter and local stdio:** pinned the official
  `github.com/modelcontextprotocol/go-sdk` v1.7.0 release, targeting modern MCP
  `2026-07-28` discovery while retaining the SDK's legacy `2025-11-25`
  initialization path. Added `lore mcp stdio`, deterministic read-only tool
  schemas and annotations for search, read, recent history, lint, and index
  health, schema-v1 structured envelopes with compact text summaries, bounded
  search/read/history inputs, managed tag/path filters shared by filesystem and
  indexed search, Lore-URI reads, safe external error mapping, and a typed core
  lint service. In-process SDK tests cover modern discovery, legacy
  initialization, every M1 tool, annotations, schemas, bounds, and protocol-only
  stdout.
- **M2 — authorization and mutation tools:** added immutable trusted principals,
  additive query/capture/curate/inspect/history permissions, fixed launcher-side
  local profiles, sensitivity-aware core operations, defensive HTTP
  `local-only` exclusion, not-found masking, mixed-history subject redaction,
  filtered lint and index aggregates, actor-owned transaction operations, and
  commit-time reauthorization. Added capture, preview, commit, transaction
  list/show/discard tools with strict schemas and accurate annotations, exact
  capture-byte handling, stable external error envelopes, bounded response
  content, and principal-scoped durable idempotency records containing only
  digests and minimal results. Cross-principal, sensitivity-change, source
  integration, retry, expiry, lock, symlink, and protocol tests cover the new
  boundary.

## Verified v0.3 baseline for v0.4

- Baseline commit: `80216c229887dbcfd9aeb7c2ce1a7154ff0c41e9`
- Baseline tag: `v0.3.0`
- Baseline date: 2026-07-29
- Working tree before v0.4 changes: clean
- `make check`: passed
- `make test-race`: passed
- `CGO_ENABLED=0 go build ./cmd/lore`: passed
- `go mod tidy -diff`: clean
- `go mod verify`: passed (`all modules verified`)
- `govulncheck ./...`: passed (`No vulnerabilities found`)

## v0.4 milestone plan

1. **SDK adapter and local stdio**
   - Pin the official Go MCP SDK, define a transport-neutral gateway seam, add
     stdio serving, read/query/inspection tools, deterministic schemas and
     annotations, and modern/legacy in-process protocol coverage.
2. **Authorization model and mutation tools**
   - Add trusted principals, additive permissions, sensitivity enforcement,
     local profiles, actor-bound capture/transaction operations,
     not-found masking, and bounded durable idempotency.
3. **Stateless Streamable HTTP**
   - Add strict external configuration, protected token-file loading,
     constant-time bearer authentication, exact origins, safe bind/plaintext
     policy, request/concurrency/time limits, health checks, and shutdown.
4. **Resources, audit, and operational hardening**
   - Add bounded ID resources, privacy-preserving audit logs, recovery and
     concurrency behavior, adversarial prompt/path tests, deployment examples,
     and MCP Inspector validation.
5. **Client matrix, docs, and v0.4 release**
   - Validate current Codex and Claude Code stdio/HTTP configurations, document
     permissions, deployment, token rotation and troubleshooting, audit
     dependencies/security, run the complete release matrix, and tag `v0.4.0`.

## Verified v0.2 baseline for v0.3

- Baseline commit: `ba9578d5f56c9073df154247cbe2892d5cbd5c3b`
- Baseline tag: `v0.2.0`
- Baseline date: 2026-07-29
- `make check`: passed
- `make test-race`: passed
- `go mod tidy -diff`: clean
- `go mod verify`: passed (`all modules verified`)
- Working tree before v0.3 changes: clean

The first baseline invocation used an empty sandbox module cache and a compiler
cache beneath a read-only home directory. After downloading the exact modules
already pinned by v0.2 and redirecting `ccache` to `/tmp`, the complete suite
passed without source changes.

## v0.3 milestone plan

1. **Schema, lifecycle, and full build**
   - Pin a pure-Go SQLite driver, probe FTS5, bind index identity to the
     repository, build through a private single-file temporary database,
     verify it, atomically replace it, and expose `index build` and `status`.
2. **Incremental maintenance**
   - Reconcile deterministic canonical scans with add/update/delete/no-op
     behavior, implement `index update` and safe idempotent `clear`, and
     classify freshness across Git, non-Git, recovery, and corruption states.
3. **Indexed search**
   - Add safe FTS candidate generation, backend selection, explicit
     sensitivity policy, scorer/snippet reuse, additive response metadata, and
     committed filesystem/index parity fixtures.
4. **Write integration and hardening**
   - Best-effort refresh existing indexes after capture and transaction commit,
     preserve canonical success on derived failure, extend lint diagnostics,
     and cover concurrency, permissions, adversarial queries, and benchmarks.
5. **Documentation and v0.3 release**
   - Update generated rules, CLI/config/index/security documentation, dependency
     and license records, benchmarks, release notes, full verification, and the
     annotated `v0.3.0` tag.

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
- v0.3 M1 `make check`: passed
- v0.3 M1 `go test -race ./...`: passed
- v0.3 M1 `CGO_ENABLED=0 go build ./cmd/lore`: passed
- v0.3 M1 `go mod tidy -diff`: clean
- v0.3 M1 `go mod verify`: passed (`all modules verified`)
- v0.3 M2 `make check`: passed
- v0.3 M2 `go test -race ./...`: passed
- v0.3 M2 `go mod tidy -diff`: clean
- v0.3 M2 `go mod verify`: passed (`all modules verified`)
- v0.3 M3 `make check`: passed
- v0.3 M3 `go test -race ./...`: passed
- v0.3 M3 retrieval parity and adversarial-query fixtures: passed
- v0.3 M3 `go mod tidy -diff`: clean
- v0.3 M3 `go mod verify`: passed (`all modules verified`)
- v0.3 M4 `make check`: passed
- v0.3 M4 `make test-race`: passed
- v0.3 M4 `CGO_ENABLED=0 go build ./cmd/lore`: passed
- v0.3 M4 `go mod tidy -diff`: clean
- v0.3 M4 `go mod verify`: passed (`all modules verified`)
- v0.3 M4 10,000-document benchmark: `809817535 ns/op`,
  `179286976 B/op`, `2221907 allocs/op`, index/text ratio `2.223`
- v0.3 M5 `make check`: passed
- v0.3 M5 `make test-race`: passed
- v0.3 M5 `CGO_ENABLED=0 go build ./cmd/lore`: passed
- v0.3 M5 `go mod tidy -diff`: clean
- v0.3 M5 `go mod verify`: passed (`all modules verified`)
- v0.3 M5 `govulncheck ./...` with v1.6.0: passed
  (`No vulnerabilities found`)
- v0.3 release build/version injection: passed
- v0.3 generated-template initialization, lint, full index verification,
  explicit non-Git indexed search, and clear smoke: passed
- v0.4 M1 `make check`: passed
- v0.4 M1 `make test-race`: passed
- v0.4 M1 `CGO_ENABLED=0 go build ./cmd/lore`: passed
- v0.4 M1 `go mod tidy -diff`: clean
- v0.4 M1 `go mod verify`: passed (`all modules verified`)
- v0.4 M1 `govulncheck ./...`: passed (`No vulnerabilities found`)
- v0.4 M2 `make check`: passed
- v0.4 M2 `make test-race`: passed
- v0.4 M2 `CGO_ENABLED=0 go build ./cmd/lore`: passed
- v0.4 M2 `go mod tidy -diff`: clean
- v0.4 M2 `go mod verify`: passed (`all modules verified`)

## v0.3 completed milestones

- M1: pinned `modernc.org/sqlite` v1.54.0, private and contained index
  paths, process-shared operation locking, FTS5 capability probing, schema and
  secure-delete configuration, Git/UUID repository identity, deterministic
  canonical scans, verified single-transaction temporary builds, atomic
  replacement with live WAL mode, lightweight/full status, strict index
  configuration, generated ignore rules, and real-SQLite integration coverage.
- M2: transactional full-scan reconciliation with exact add/update/delete/no-op
  counts, unchanged-row preservation, same-transaction FTS maintenance and
  verification, snapshot revalidation, Git/non-Git freshness classification,
  manifest verification, wrong-identity/corruption/incompatibility detection,
  and idempotent contained clear with derived-symlink rejection.
- M3: `auto`/`index`/`filesystem` backend selection, explicit sensitivity
  policy, safe quoted FTS expressions, conservative short-query fallback,
  bounded FTS5 candidate generation, unchanged Go scoring/snippet/tie logic,
  additive backend response metadata, stale explicit-index refusal, and
  real-index parity/security/large-result fixtures.
- M4: best-effort updates of existing compatible indexes after durable capture
  and transaction writes, generic non-fatal refresh diagnostics, real-tree
  lint warnings for derived health/tracking/symlinks/permissions, deterministic
  pre-replacement/pre-commit fault hooks, rollback preservation, WAL reader and
  exclusive-operation concurrency coverage, and a reproducible 10,000-document
  build benchmark.
- M5: v0.3 command/configuration/index lifecycle/security/recovery
  documentation, generated agent rules for derived-index handling, exact
  runtime dependency and selected-graph license inventory, benchmark results,
  release notes, vulnerability and module-integrity audits, pure-Go build, and
  generated-repository lifecycle smoke coverage.

v0.3 milestone commits:

- M1: `719f9c2`
- M2: `a27280d`
- M3: `80ee3b6`
- M4: `868fc90`
- M5: the release commit tagged `v0.3.0`

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

- No known v0.3 correctness issues.
- v0.3 has no automatic transaction-artifact retention or pruning.
- A local index intentionally duplicates canonical text and can be larger than
  the source corpus; it is optional and disposable.
- Automatic search uses the filesystem for non-Git repositories because Git
  cannot cheaply certify freshness. Explicit indexed search performs a full
  manifest comparison.
- The supported and fully tested target is Linux with Git available. The index
  operation lock uses Linux `flock`.

## Material deviations and compatibility notes

- New-file publication uses a same-directory hard-link/no-clobber publish followed by temporary-link removal instead of a replacing rename. Updates use flushed same-directory temporary files and rename.
- Unified diffs use a narrowly wrapped `git diff --no-index` operation rather than a new Go dependency.
- Discarded transactions retain proposal/state receipt metadata and a lint summary; full content, diff, and lint payload artifacts are removed.
- Knowledge-repository `lore.yaml` remains at `version: 1`. `git.auto_push_transactions` is optional and defaults to `false`; strict v0.1 binaries reject a configuration that includes the new key.
- The v0.3 representative index schema includes deterministic alias/tag JSON
  and body-line metadata so existing scorer/snippet code consumes exact
  structured candidates.
- Full FTS integrity verification remains an explicit status/build/update
  operation. Indexed search uses read-only state and non-Git manifest checks so
  readers can continue during a WAL update.
- The new optional `index` configuration block receives defaults when absent;
  strict v0.2 binaries reject it during mixed-version operation.

## Next checkpoint

Future work begins from the exact commit tagged `v0.3.0`. Preserve the v0.3
Markdown authority, JSON schema, lexical parity, derived-index, transaction,
and recovery contracts before adding a new milestone.
