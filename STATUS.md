# Lore implementation status

## Current release and milestone

Lore v0.4.0 — released from the final status commit tagged `v0.4.0`.
Milestones 1–5 and the complete release matrix are finished.

## Post-v0.4 maintenance

- **Repository-safe Codex MCP launch guidance:** completed 2026-08-07. Codex
  examples now separate portable project-scoped MCP identity and enablement
  from a machine-local absolute working directory. This accounts for the
  persistent remote-control app server resolving a relative MCP `cwd` from its
  own launch context, which can make a valid Lore child exit during initialize
  or attach it to the wrong repository. Machine-local definitions remain
  disabled outside their matching project; the trusted project layer enables
  only its unique server identity, and preflight still verifies the canonical
  repository root before use.
- **Lower-friction agent reading and editing:** completed 2026-08-05. Added
  authorization-filtered MCP `lore_read_many`, which preserves request order
  while reading 1–8 independently ranged documents after one catalog scan and
  enforces per-item and aggregate response bounds. Added revision-guarded
  `patch_page` transactions for 1–50 exact, unique, non-overlapping text
  replacements matched against the original page. Patches still materialize
  the complete prospective page and pass the existing metadata, full-diff,
  lint, digest, commit, recovery, Git, and authorization contracts. Safe MCP
  diagnostics identify missing, ambiguous, or overlapping replacement indexes
  without echoing caller or repository content. Shared operating guidance now
  prefers bounded batch reads for several likely results, exact patches for
  small localized edits, and whole-page updates for substantial rewrites.
  Authorized reads now also filter inaccessible documents before alias/title
  ambiguity resolution, preventing protected candidate paths from leaking.
  `go vet ./...`, `go test ./...`, `go test -race ./...`, and
  `go build ./cmd/lore` all pass.
- **Actionable MCP transaction diagnostics:** completed 2026-08-04. Safe
  semantic validation now reports `integrated_page_missing`, the exact
  `operations[].page_ids` field, and a bounded array containing only invalid
  IDs supplied by the caller; malformed or unlisted validation details still
  fail closed to the generic public error. Missing and cross-actor transactions
  remain deliberately indistinguishable, but now return transaction-specific
  same-actor/interface guidance instead of a misleading document-not-found
  message. A missing transaction cannot be distinguished from another actor's
  transaction through commit. Shared operating rules and MCP documentation now
  clarify that new integration IDs must resolve prospectively while existing
  ledger IDs may outlive their pages, and that switching between CLI and MCP
  requires a fresh preview.
  `go vet ./...`, `go test ./...`, `go test -race ./...`, and
  `go build ./cmd/lore` all pass.
- **Living-page structural maintenance:** completed 2026-08-04. Synthesized
  pages can now be revision-guarded deletions or change IDs during whole-page
  updates. Page moves, consolidation, splits, and replacements compose from
  generic create/update/delete operations inside the existing atomic
  preview/digest/commit/recovery contract. New CLI `lore references` and MCP
  `lore_page_references` return authorization-filtered live page backlinks,
  immutable historical source-body mentions, and additive source-integration
  records. Prospective lint requires live synthesized-page backlinks to be
  repaired in the same transaction, while raw source links and historical
  `integrated_into` IDs never block later reorganization. Delete-aware artifact
  storage, diffs, recovery rollback/finalization, Git verification, pruning,
  index refresh, and permission checks preserve the existing safety model.
  Generated operating rules and MCP initialization instructions now describe
  pages as a living current view and explicitly guide structural maintenance.
  Focused end-to-end tests cover an atomic recipe reorganization, broken-live-
  backlink rejection, source-history preservation, rekeying, crash recovery,
  pruning, CLI/MCP adapters, and authorization masking.
  `go vet ./...`, `go test ./...`, `go test -race ./...`, and
  `go build ./cmd/lore` all pass.
- **Review backlog item 1 — deterministic MCP test clock:** completed
  2026-07-30. The shared MCP integration fixture now uses an explicit fixed UTC
  clock aligned with its dated documents, removing a wall-clock-dependent
  `updated_too_old` failure in the transaction authorization test without
  changing production behavior. The focused test passed 20 consecutive runs;
  `go vet ./...`, `go test ./...`, `go test -race ./...`, and
  `go build ./cmd/lore` all passed.
- **Review backlog item 2 — current development instructions:** completed
  2026-07-30. Root `AGENTS.md` now treats the released v0.4 CLI, disposable
  index, stdio MCP, and stateless HTTP MCP gateway as the architecture baseline
  while requiring explicit scope for genuinely new architectural surfaces.
  Generated knowledge-repository agent rules were audited and remain current;
  their focused initialization test and the full vet, test, race-test, and
  build checks passed.
- **Review backlog item 3 — crash-safe repository write lock:** completed
  2026-07-30. The removable lock directory is now a persistent private regular
  file protected by Linux `flock`, so descriptor closure on normal exit,
  `SIGKILL`, or OOM termination releases ownership without operator cleanup.
  Writers use a deterministic two-second, context-cancellable exponential
  retry window; contention retains typed, body-free diagnostics. Migration
  waits for a live v0.4 directory-lock writer to finish, fails closed on a
  persistent legacy directory, and prevents mixed-version double ownership.
  Subprocess-death, live-contention, cancellation, malformed-metadata,
  symlink/non-regular-path, upgrade, core error-shape, and queued MCP-writer
  tests pass. `go vet ./...`, `go test ./...`, `go test -race ./...`, and
  `go build ./cmd/lore` all passed.
- **Review backlog item 4 — hardened Git subprocess execution:** completed
  2026-07-30. Every Git invocation now uses one noninteractive runner with a
  deterministic environment allowlist, command-line hardening, 30-second local
  and two-minute push deadlines, earlier-caller deadline precedence, and Linux
  process-group termination on cancellation. Repository hooks, filesystem
  monitors, signing, automatic maintenance, external transports, editors,
  pagers, prompts, and askpass programs are disabled. Active Git content
  filters on managed or staged paths are rejected before status/add can execute
  them. Unattended pushes retain explicitly documented support for narrow SSH
  deploy keys or agents and trusted noninteractive HTTPS credential helpers.
  Adversarial tests cover hooks, signing, fsmonitor, filters, environment
  leakage, askpass, credential helpers, local/network/caller timeouts,
  cancellation, and surviving child processes. `go vet ./...`,
  `go test ./...`, `go test -race ./...`, and `go build ./cmd/lore` all passed.
- **Review backlog item 5 — safe transaction-artifact retention:** completed
  2026-07-30. Added the explicit local `lore transaction prune` command with a
  required whole-hour/day/week age, bounded oldest-first selection, exact UTC
  cutoff, dry-run, and deterministic schema-v1 reporting. Only committed
  transactions are eligible, using immutable `committed_at`; active recovery
  and every noncommitted state are protected. Prune holds the repository write
  lock, preflights every selection before removal, requires the recorded commit
  to remain reachable, verifies its exact path set and blob hashes, then
  revalidates each transaction immediately before compaction. A private
  two-phase `retention.json` receipt binds an exact sorted payload manifest and
  makes cancellation or interruption resumable while preserving proposal/state
  receipts, local inspection, and repeated-commit idempotency. Removal is
  limited to hash-verified regular content, diff, and lint files; strict
  layouts, no-follow reads, symlink rejection, and lint findings fail closed.
  No repository retention config, MCP mutation, bundled timer, or secure-erasure
  claim was added. Store, core, CLI, Git-proof, cutoff/limit, recovery/lock,
  authorization, lint, cancellation, interruption, malformed-layout, and
  symlink tests pass. `go vet ./...`, `go test ./...`,
  `go test -race ./...`, and `go build ./cmd/lore` all passed.
- **Review backlog item 6 — bounded HTTP memory envelope:** completed
  2026-07-30. External configuration now caps both configured in-flight
  request capacity and response capacity at 64 MiB using overflow-safe
  cross-field validation while preserving the 8 MiB/eight-request defaults.
  Lore's bounded writer now runs before the standard-library timeout buffer,
  preventing that inner buffer from growing past the response cap.
  Whole-response publication no longer advertises flushing and rejects SSE
  bodies, making Lore's finite stateless-JSON transport assumption executable.
  The deployment guide distinguishes payload capacity from process RSS, and
  the sample systemd unit adds 384 MiB `MemoryHigh` and 512 MiB `MemoryMax`
  starting limits for modest default deployments. Boundary, overflow, and SSE
  regression tests pass; `go vet ./...`, `go test ./...`,
  `go test -race ./...`, and `go build ./cmd/lore` all passed. The sample unit
  parsed under `systemd-analyze verify` with only the expected absent
  `/usr/local/bin/lore` deployment-path warning.
- **Review backlog item 7 — lazy HTTP resource enumeration:** completed
  2026-07-30. Query-capable stateless servers now register only their stable
  page/source templates during construction and enumerate concrete authorized
  pages immediately before `resources/list`. Discovery, initialization, tool
  and template listing, exact resource reads, and unrelated tool calls no
  longer pay a resource-registration catalog scan. HTTP uses no cross-request
  resource metadata cache, so external sensitivity reclassification is visible
  on the next list; exact reads continue to reauthorize current Markdown.
  HTTP accurately advertises no resource-list notifications, scan failures are
  generic and retryable, concurrent lists on one server share one load, and
  stateless commits skip their previously wasted post-success refresh. Stdio
  retains eager startup enumeration and refreshes loaded resources after
  commit. On the development host, five-iteration 1,000/10,000-document
  benchmarks held HTTP server construction roughly flat at 0.52/0.48 ms,
  measured intentional cold-registry enumeration at 14.6/120.6 ms, and measured
  warm stdio registry checks at 60/70 ns. Focused tests passed 20 consecutive
  runs; `go vet ./...`, `go test ./...`, `go test -race ./...`, and
  `go build ./cmd/lore` all passed.
- **Review backlog item 8 — meaningful bounded readiness:** completed
  2026-07-30. Liveness remains process-only and performs no repository I/O.
  Readiness now verifies the startup repository identity and configuration,
  five required real directories, and absence of active recovery using a fixed
  set of symlink-rejecting metadata/open checks. It never scans or parses managed
  Markdown, reparses YAML, invokes Git, inspects the index, contacts a remote,
  or acquires the write lock. Repository degradation, configuration drift, and
  active or malformed recovery state return the same metadata-free
  `503 {"status":"unavailable"}` while liveness stays healthy; resolving
  recovery restores readiness without restart. Configuration changes require a
  restart so the running service and readiness baseline agree. Root/config
  replacement, required-path damage, recovery lifecycle, privacy equivalence,
  document-scan exclusion, and method-ordering tests pass. Focused tests passed
  20 consecutive runs; `go vet ./...`, `go test ./...`,
  `go test -race ./...`, and `go build ./cmd/lore` all passed.
- **Review backlog item 9 — safe remote deployment examples:** completed
  2026-07-30. Tailscale Serve over Lore's loopback listener is now the
  recommended private topology, with a query-only/normal-only principal,
  least-privilege TCP-443 grant and policy tests, exact `/mcp` publication,
  persistent Serve setup, and explicit prohibition on Funnel. A separate
  Caddy 2.10+ custom-domain example preserves bearer authorization, limits
  request bodies and connection phases consistently with Lore's defaults,
  omits access/body/credential logging, routes only `/mcp`, and leaves health
  endpoints local. The deployment guide now distinguishes both trust
  boundaries; covers DNS, certificates, firewalls, identity headers,
  restarts, direct-bind fallback risk, and post-reboot smoke checks; and keeps
  public reachability non-default. The Lore configurations passed strict
  config validation, the Caddyfile passed Caddy 2.11.4 validation, and the
  full vet, test, race-test, and build checks passed.
- **Review backlog item 10 — bounded authenticated request rate:** completed
  2026-07-30. Each configured HTTP principal now receives an independent
  in-memory token bucket admitting 128 immediate requests and sustaining 600
  requests per minute by default. Admission occurs after exact-origin and
  constant-time bearer authentication but before shared concurrency, MCP
  parsing, or repository work. Bucket state is fixed at configured principals;
  health, origin denials, and invalid tokens neither allocate nor consume it.
  Exhaustion returns deterministic private/no-store HTTP `429` with an integer
  `Retry-After`. Strict configuration supports deliberately higher or lower
  limits, requires a burst at least as large as configured concurrency, and
  preserves defaults when the new block is omitted. Expensive-operation
  weighting was deliberately deferred: the generous method-agnostic fuse
  protects tight loops without coupling admission to MCP bodies or constraining
  ordinary agent workflows. Isolation, refill, full-default-burst, concurrent
  safety, bounded-state, origin/authentication/health exclusion,
  response-privacy, configuration-boundary, and normal protocol tests pass.
  `go vet ./...`, `go test ./...`, `go test -race ./...`, and
  `go build ./cmd/lore` all passed.
- **Review backlog item 11 — privacy-safe authentication-denial
  observability:** completed 2026-07-30. Lore denial warnings now distinguish
  only `missing_credentials` from `invalid_credentials` while preserving an
  identical public `401` and excluding direct/forwarded addresses, attempted
  identities, tokens and hashes, headers, targets, queries, and bodies. Source
  attribution remains at the edge: the default private and public examples
  retain no access log, while a separate opt-in Caddy example emits a minimized
  private rolling record containing only fixed logger fields, timestamp, direct
  source IP, constant `/mcp`, and status. It skips unrelated paths and never
  trusts forwarded identity. A disabled-by-default Fail2ban jail consumes only
  that Caddy record, uses IP-only targets and forgiving 30-in-five-minute/
  15-minute-ban defaults, and documents proxy, retention, recovery, and
  self-ban hazards. Seeded-secret, reason-shape, response-equivalence, and
  existing audit tests pass. The Caddyfile validated and its runtime output
  matched the documented minimized shape under Caddy 2.11.4. Both Caddy
  examples also remove request metadata from the separate runtime-error stream,
  including during seeded upstream failure. The filter matched only IPv4/IPv6
  Caddy `401` fixtures and the enabled jail configuration parsed under upstream
  Fail2ban 1.1.0. `go vet ./...`, `go test ./...`, `go test -race ./...`, and
  `go build ./cmd/lore` all passed.
- **Review backlog item 15 — focused failure-path coverage:** completed
  2026-07-30 without a percentage-driven sweep. Recovery-store tests now reject
  active-path and artifact symlinks, modified originals, unknown journal
  fields, and trailing JSON values; a failed journal creation proves it leaves
  neither published nor temporary state. End-to-end CLI tests preserve stable
  exit codes for missing recovery actions, map malformed journals to the
  generic runtime error, and prove private journal content stays out of JSON
  and stderr. The earlier operational items already cover Git
  prompt/hook/timeout/process-group behavior, dead lock owners and bounded
  waiting, prune interruption and malformed artifacts, external-edit recovery
  conflicts, and graceful HTTP shutdown. Filesystem permission and disk-full
  injection remain intentionally deferred until a portable deterministic
  harness exists. Focused recovery/app tests and the full vet, test, race-test,
  and build checks passed.
- **Review backlog item 16, phase 1 — measured retrieval baseline:** completed
  2026-07-31 without changing production search behavior. Added a strict,
  repository-owned agent-retrieval harness with an 18-document synthetic
  Markdown corpus and 49 graded cases covering direct terms, noisy natural
  questions, morphology, typos, vocabulary mismatch, multi-term ranking,
  metadata filters, sources, and sensitivity boundaries. Each run stages the
  corpus privately, builds a disposable index without depending on developer
  Git state, evaluates forced filesystem and production `auto` search, and
  reports hit@1/3/5/10, MRR, recall@5, nDCG@10, result volume, zero results,
  forbidden results, effective backend use, and exact parity. A checked-in
  deterministic JSON baseline is enforced by `go test ./...`; the developer
  command prints weak cases and requires an explicit reviewed baseline update.
  The initial production-auto baseline is 63.3% hit@1, 69.4% hit@5, 0.650 MRR,
  and zero forbidden results. Direct, metadata, source, multi-term, and
  authorization cases are perfect; typo and vocabulary-mismatch cases are
  zero, morphology is 33.3% hit@5, and noisy natural questions are 90% hit@5.
  The harness also found one pre-existing non-top-result parity gap: filesystem
  scoring can return kind-only matches that FTS candidate generation cannot
  discover because `kind` is absent from the FTS table. Lexical, fuzzy,
  result-evidence, and agent-search-loop improvements remain the next phase.
- **Review backlog item 16, phase 2 — lexical ranking quality:** completed
  2026-07-31. The shared deterministic scorer now gives exact token credit to
  individual tags, rewards distinct query-term coverage, and applies bounded
  corpus-rarity bonuses computed only over authorized documents remaining
  after scope, kind, tag, and path filters. Exact phrase/title boosts remain
  dominant and repeated body occurrences remain capped. Filesystem search now
  feeds the same candidate ranker used by indexed search. Derived index schema
  version 2 adds `kind` to FTS candidates and an indexed exact-term table
  populated by Lore's Unicode tokenizer; this supplies corpus-wide frequency
  data even when FTS candidates are capped and avoids FTS diacritic
  normalization changing public scores. Existing schema-version-1 indexes are
  intentionally incompatible and require a disposable rebuild; public JSON
  and repository configuration remain schema version 1. Focused tests cover
  coverage versus repetition, rarity, separate tag tokens, capped candidates,
  kind-only discovery, and accent-sensitive backend parity. On the unchanged
  49-case suite, hit@1 held at 63.3%, hit@5 improved from 69.4% to 75.5%, MRR
  from 0.650 to 0.679, nDCG@10 from 0.660 to 0.698, and zero-result cases fell
  from 11 to 10. Morphology hit@5 rose from 33.3% to 50.0% and vocabulary
  mismatch from 0% to 33.3%; fuzzy typos remain 0%. Forbidden results remain
  zero and filesystem/production-auto parity improved from 48/49 to 49/49.
  Fuzzy candidate generation, richer result evidence, and agent search-loop
  guidance remain separate follow-on work.
- **Review backlog item 16, phase 3 — adaptive fuzzy matching:** completed
  2026-07-31. Search now defaults to backend-independent `matching=auto`, which
  keeps exact lexical behavior and typo-expands only authorized,
  already-filtered out-of-vocabulary query terms of at least six Unicode
  characters, up to a length bound of 24. Callers can select `lexical` for
  strict exact-token behavior or `fuzzy` to expand all eligible terms from
  four through 24 characters. Expansion
  uses deterministic bounded Unicode Damerau-Levenshtein distance, minimum
  similarity thresholds, at most eight query terms, and at most four
  corrections per term. Exact evidence remains stronger, fuzzy phrase scoring
  is excluded, and every returned correction includes its query term,
  document term, and edit distance. Filters are applied before vocabulary
  construction, preventing unauthorized terms from appearing in corrections
  or frequency statistics. Derived index schema version 3 adds exact Unicode
  rune lengths to the term table; schema versions 1 and 2 require the normal
  disposable rebuild. A 100,000-term filtered-vocabulary work bound makes
  automatic search fall back to exact results with a warning and makes
  explicit fuzzy search fail instead of silently returning partial evidence.
  Focused tests cover transpositions, Unicode, deterministic selection,
  scoring, backend parity, access isolation, schema incompatibility and
  corruption, CLI/MCP contracts, work-bound behavior, and allocation-bounded
  candidate selection. The optimized 100,000-term selection microbenchmark on
  the development workstation runs in about 62 ms with 384 bytes and one
  allocation, excluding SQLite vocabulary materialization. On the 49-case
  retrieval suite, hit@1 is 79.6%, hit@5 is 89.8%, MRR is 0.838, nDCG@10 is
  0.854, zero-result cases fall from 10 to 2, forbidden results remain zero,
  and filesystem/production-auto parity remains 49/49. All four dedicated
  typo cases now hit the expected page at rank 1; remaining failures are
  primarily semantic vocabulary mismatches rather than spelling errors.
  `go vet ./...`, `go test ./...`, `go test -race ./...`, and
  `go build ./cmd/lore` all passed.
- **Review backlog item 16, phase 4 — agent retrieval loop and search v1:**
  completed 2026-07-31. Generated AGENTS, Claude, operating-rule, and repository
  guidance now teaches agents to start with automatic matching, inspect and
  read likely results, reformulate weak queries with distinctive evidence
  terms and metadata filters, use fuzzy matching for uncertain spelling,
  reserve lexical matching for exact verification, and never infer absence
  from one zero-result query. Authorized local agents may complement Lore with
  `rg` over authoritative Markdown; MCP-only agents may not bypass principal
  permissions or sensitivity policy. The MCP documentation carries the same
  bounded loop, and the evaluation guide now accurately describes the current
  lexical-plus-fuzzy baseline. Search v1 is considered complete; further
  ranking or semantic work should be driven by observed agent failures added
  to the retrieval harness rather than speculative expansion. Fresh-repository
  initialization tests, `go vet ./...`, `go test ./...`, and
  `go build ./cmd/lore` all passed.
- **Post-review follow-up — bounded HTTP response publication:** completed
  2026-07-31. Authenticated MCP requests now retain their global concurrency
  slots through both bounded whole-response buffers and the final client write,
  preventing slow readers from accumulating response buffers outside the
  configured concurrency envelope. A production-composition regression test
  blocks the final write and proves a second request receives `429` without
  entering MCP. Deployment guidance now distinguishes bounded response capacity
  from repository-scan working sets, requires readiness alerting because
  `Restart=on-failure` cannot observe a readiness-only `503`, keeps health paths
  private, recommends idle low-limit transaction pruning, and records the
  one-way `rate_limit` configuration compatibility boundary. Retention artifact
  validation now requires exactly three ASCII digits and reports malformed
  trailing JSON consistently with strict repository metadata parsing. Focused
  tests passed 20 consecutive runs; `go vet ./...`, `go test ./...`,
  `go test -race ./...`, and `go build ./cmd/lore` all passed.
- **Dogfood follow-up — truthful index warnings and portable curation
  guidance:** completed 2026-07-31. Transaction commit no longer carries the
  expected pre-commit `index_stale` lint finding into a successful result;
  `index_refresh_failed` is now reserved for an actual failed post-commit
  refresh, while other derived-index findings map to `index_health_warning`.
  MCP initialization and generated repository guidance now teach minimally
  self-contained context for approvals, canonical shared-subject pages instead
  of duplicated profile facts, and evidence-qualified resolution of relative
  dates. Only that generic temporal rule was incorporated from the dogfood
  repository; no personal guidance was copied. Idempotency guidance now makes
  keys explicitly optional and assigns stable retry-key generation to clients.
  Revision-guarded partial-page operations remain deferred pending additional
  whole-page editing failures from dogfooding. Focused regressions passed 20
  consecutive runs; `go vet ./...`, `go test ./...`, `go test -race ./...`,
  and `go build ./cmd/lore` all passed.
- **Dogfood follow-up — actionable UTC page-update validation:** completed
  2026-07-31. MCP retains the backward-compatible `invalid_argument` category
  while `updated_too_old` now carries a whitelisted stable reason and sanitized
  `field`, `path`, and UTC `minimum` details. Other validation failures remain
  generic by default. Preview tool, initialization, data-model, CLI, MCP, and
  generated agent guidance now explain the UTC page-date contract and how to
  recover when a client remains on the preceding local date. Regression
  coverage exercises the America/New_York-to-UTC midnight boundary and proves
  error results disclose neither page bodies nor unlisted validation details.
  Generic MCP and generated guidance separately defines the user-time semantic
  clock: use a known user timezone for human meaning, preserve explicit source
  timezones, ask when an unknown timezone is material, and never treat Lore's
  UTC metadata date as the user's local date. First-use guidance checks
  authorized context for preferred name and default timezone, asks instead of
  guessing when either is unknown, and retains answers only with consent.
  Focused regressions passed 20 consecutive runs; `go vet ./...`,
  `go test ./...`, and `go build ./cmd/lore` all passed.
- **Dogfood follow-up — truthful source-integration warnings:** completed
  2026-07-31. Commit-time lint no longer publishes the expected temporary
  `uncommitted_source_change` findings for sources being changed by the active
  `mark_source_integrated` transaction. Exact-path filtering retains the same
  warning for independently dirty sources, which MCP now reports as the
  specific `source_worktree_dirty` code rather than `operation_warning`.
  Focused regressions passed 20 consecutive runs; `go vet ./...`,
  `go test ./...`, `go test -race ./...`, and `go build ./cmd/lore` all
  passed.
- **Instruction hierarchy cleanup:** completed 2026-08-03. Generated
  `system/OPERATING_RULES.md` is now the single shared Lore policy,
  repository-specific owner context and session procedures belong only in
  root `AGENTS.md`, and `CLAUDE.md` is a native import passthrough for both.
  Initialization regression coverage prevents shared policy from drifting back
  into `AGENTS.md` or `CLAUDE.md`. Existing repositories adopt template
  changes through an explicit protected-file maintenance change; Lore does not
  overwrite local instruction files during normal operation. A post-pull
  dogfood review restored explicit defenses against privilege claims through
  tool arguments, sensitivity downgrades, and unnecessary collection of
  personal defaults. It also makes Lore tools mandatory for supported
  repository mutations and administration while preserving narrow exceptions
  for read-only local retrieval, Git synchronization, protected-file
  maintenance, and explicitly requested work outside the repository.
- **Dogfood follow-up — explicit capture classification and correctable source
  sensitivity:** completed 2026-08-03. CLI, core, and MCP capture now require
  an explicit `normal`, `sensitive`, or `local-only` value instead of silently
  defaulting to `normal`. The revision-guarded `set_source_sensitivity`
  transaction operation preserves exact source bodies, `raw_sha256`, and
  unrelated frontmatter while using the normal preview, digest, authorization,
  commit, recovery, and index-refresh path. Less-restrictive changes require an
  explicit `allow_downgrade` acknowledgment. Generated and MCP guidance also
  distinguishes routine index verification from troubleshooting.
- **Dogfood follow-up — single-operation session preflight:** completed
  2026-08-05. The new typed core preflight and `lore preflight --sync` replace
  the multi-command, multi-round-trip clone startup ritual. Preflight holds the
  repository writer lock while it fails closed on the wrong branch, a dirty
  full worktree, active recovery, or any pending preview; fetches the
  configured branch exactly once; fast-forwards only strictly-behind history
  from the fetched tracking ref; and reconciles the disposable index with lint
  and full verification when required. Ahead and diverged histories remain
  explicit blockers. An unchanged HEAD with a fresh certified index takes the
  lightweight path, while `--deep` preserves an on-demand full audit. The
  structured local-full stdio `lore_preflight` tool exposes the same operation,
  defaults to synchronized operation, and now accepts explicit `sync: false`
  for an intentionally local-only repository with no remote. It remains absent
  from HTTP and local-query profiles and fixes the expected branch to `main`;
  generated and MCP instructions distinguish local-only from synchronized
  writable sessions.
- **Dogfood follow-up — fail-closed multi-repository MCP identity:** completed
  2026-08-06. A Codex remote session demonstrated that a client can retain an
  already-running stdio server by configured name while its selected Git
  working directory changes. Preflight now returns and prints the canonical,
  symlink-resolved `repository_root`; its MCP summary and server instructions
  require agents to compare that root with the intended repository before any
  other Lore operation. Generated policy and MCP documentation require a
  unique project-scoped server identity for each writable Lore repository and
  prohibit a writable user-global fallback. The preflight tool remains local
  stdio only, so this path disclosure does not expand the HTTP surface.

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
- **M3 — stateless Streamable HTTP:** added strict external YAML configuration
  and `lore mcp check-config`, protected regular-file token loading, fixed-size
  token digests, constant-time bearer matching, per-request HTTP principals,
  and `lore mcp serve` with the official SDK's stateless modern transport.
  Exact origin matching, explicit IP bind policy, loopback defaults,
  non-loopback plaintext refusal, forwarded-header refusal, request/concurrency/
  timeout/response bounds, private/no-store responses, minimal health endpoints,
  SIGINT/SIGTERM handling, and bounded graceful shutdown make the network
  boundary fail closed. Hermetic HTTP/SDK tests cover authentication failure
  equivalence, principal-specific discovery, current-protocol calls, stateless
  operation, local-only exclusion, origin and size limits, cancellation,
  concurrency exhaustion, response bounds, health privacy, and active-request
  shutdown.
- **M4 — resources, audit, and operational hardening:** added authorized,
  deterministic page-resource listing with private zero-TTL pagination,
  canonical ID-only page/source templates, bounded exact resource reads through
  the shared authorized core path, additive resource URIs in search results,
  and post-commit resource refresh. Added metadata-only structured audit events,
  generic authentication-denial events, shared request correlation IDs, and
  redacted panic recovery. Seeded-secret tests cover tokens, queries, capture
  bodies, preview diffs, unauthorized titles and paths, prompt injection,
  protected-path intent, URI confusion, tool-name injection, active recovery,
  single-writer contention, concurrent reads, and shutdown. Hardened loopback,
  Tailscale Serve, Caddy, and systemd deployment examples are included under
  `docs/examples/`.
- **M5 — client matrix, documentation, and release candidate:** validated the
  current Codex and Claude Code stdio and HTTP configuration forms against
  vendor documentation and installed clients, then completed all four
  end-to-end workflows in disposable repositories. Official MCP Inspector
  v1.0.1 passed tool, resource, schema, annotation, cache, and ID-URI checks
  through both transports. A client-discovered resource-URI/read mismatch was
  reproduced, fixed in the shared core read resolver, and regression-tested.
  Added complete MCP, external configuration, security, deployment,
  permissions/sensitivity, token-rotation, recovery, troubleshooting,
  dependency/license, generated-rule, and release documentation. The hardened
  sample unit passed systemd offline analysis at exposure `2.8 OK`; a
  disposable systemd-managed service passed startup, both health checks, and
  clean shutdown.

v0.4 milestone commits:

- M1: `fa008b9`
- M2: `9cb1935`
- M3: `4790fef`
- M4: `3ebc405`
- M5: `21982eb`

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
- v0.4 M3 `make check`: passed
- v0.4 M3 `make test-race`: passed
- v0.4 M3 `CGO_ENABLED=0 go build ./cmd/lore`: passed
- v0.4 M3 `go mod tidy -diff`: clean
- v0.4 M3 `go mod verify`: passed (`all modules verified`)
- v0.4 M4 `make check`: passed
- v0.4 M4 `make test-race`: passed
- v0.4 M4 `CGO_ENABLED=0 go build ./cmd/lore`: passed
- v0.4 M4 `go mod tidy -diff`: clean
- v0.4 M4 `go mod verify`: passed (`all modules verified`)
- v0.4 M5 `gofmt -l .`: clean
- v0.4 M5 `go vet ./...`: passed
- v0.4 M5 `go test ./...`: passed
- v0.4 M5 `go test -race ./...`: passed
- v0.4 M5 `go build ./cmd/lore`: passed
- v0.4 M5 `CGO_ENABLED=0 go build ./cmd/lore`: passed
- v0.4 M5 `go mod tidy -diff`: clean
- v0.4 M5 `go mod verify`: passed (`all modules verified`)
- v0.4 M5 `govulncheck ./...` with v1.6.0 and database updated
  2026-07-27T20:14:16Z: passed (`No vulnerabilities found`)
- v0.4 M5 official MCP Inspector v1.0.1 stdio and authenticated HTTP:
  passed tools/list, resources/list, schemas, annotations, private cache
  metadata, and ID-resource-URI reads. Stable v1 emits its upstream
  deprecation notice while Inspector v2 remains a release candidate.
- v0.4 M5 Codex CLI 0.145.0 stdio and authenticated HTTP workflows: passed
  search/read, capture/search/read, preview/show/commit/read, and masked reads.
- v0.4 M5 Claude Code 2.1.217 Sonnet stdio and authenticated HTTP workflows:
  passed the same matrix. The default model first reported the account's
  monthly spend limit; selecting Sonnet completed the checks.
- v0.4 M5 current Codex and Claude Code isolated registration/connection
  checks: passed. Claude Code's current `mcp get` can display a configured
  static header value, so the troubleshooting guide directs operators to
  privacy-safe status surfaces.
- v0.4 M5 systemd 259: sample unit parsed with only the expected missing
  `/usr/local/bin/lore` workstation warning; offline exposure `2.8 OK`.
  A disposable user service passed startup, live/ready health checks, stop,
  and auto-collection. Its first harness attempt failed because `PrivateTmp`
  correctly hid the `/tmp`-hosted binary; the successful harness omitted that
  property, while the shipped `/usr/local` unit keeps it enabled.
- v0.4 M5 release build injected version `0.4.0`, full commit
  `21982ebe5e9a121f952b7f3fb23cc8a9cc97e7cc`, and UTC build time successfully.
- v0.4 M5 generated-template initialization, lint, verified full index build,
  explicit indexed empty-result search, and derived-index clear: passed.

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

- No known v0.4 correctness issues.
- Transaction-artifact retention is explicit and local; Lore has no automatic
  repository retention policy or bundled timer.
- A local index intentionally duplicates canonical text and can be larger than
  the source corpus; it is optional and disposable.
- Automatic search uses the filesystem for non-Git repositories because Git
  cannot cheaply certify freshness. Explicit indexed search performs a full
  manifest comparison.
- The supported and fully tested target is Linux with Git available. The index
  operation lock uses Linux `flock`.
- Lore v0.4 does not provide OAuth, hosted-client reachability, multi-repository
  routing, a public TLS listener, or a server-side LLM. Use a private transport
  or TLS/authenticated reverse proxy for HTTP.

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
- HTTP MCP configuration is a separate strict schema-version-1 file so
  canonical knowledge cannot grant itself network or principal capabilities.
- The built-in HTTP listener is plaintext. Non-loopback startup requires an
  explicit exact IP and explicit override intended only for an independently
  encrypted and access-controlled private network.
- HTTP principals can never include `local-only`. The two fixed local stdio
  profiles include all local sensitivities; an unknown local read supplies the
  not-found-shape client smoke while actual sensitivity masking is exercised
  over HTTP and in automated cross-principal tests.
- Official Inspector verification pinned the then-current stable v1.0.1 rather
  than executing an unpinned package tag. Upstream labels v1 deprecated and had
  published v2 only as a release candidate on the verification date.

## Next checkpoint

Future work begins from the exact commit tagged `v0.4.0`. Preserve the Markdown
authority, JSON schema, lexical parity, derived-index, transaction,
authorization, MCP, audit, and recovery contracts before adding a new
milestone.
