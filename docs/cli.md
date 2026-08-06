# Lore CLI reference

## General behavior

```text
lore [--repo PATH] [--json] <command> [options]
```

Repository resolution uses `--repo`, then `LORE_REPO`, then an upward search for `lore.yaml`. `init` is the exception and uses its positional path or the current directory.

Commands are non-interactive. Normal results go to stdout, while diagnostics, warnings, and the identifying header for human-mode `read` go to stderr. JSON mode emits one undecorated object on stdout. Every JSON response contains `schema_version: 1`, uses `snake_case` keys, and is deterministic for the same repository state.

JSON errors have this shape:

```json
{
  "schema_version": 1,
  "error": {
    "code": "ambiguous_reference",
    "message": "reference matched more than one document",
    "details": {
      "candidates": []
    }
  }
}
```

Exit codes are stable:

| Code | Meaning |
|---:|---|
| 0 | success |
| 1 | validation or lint findings |
| 2 | usage or configuration error |
| 3 | filesystem, Git, or integrity operation failure |
| 4 | conflict, ambiguity, or lock contention |

All commands support `--help`.

Repository writers use the persistent regular file `.lore/write.lock` as a
Linux `flock` target. They wait for up to two seconds with context-aware
backoff before returning the typed `repository_locked` conflict. The lock file
records only diagnostic PID, hostname, command, and start-time metadata; it
remains in place when unlocked. Process exit, including `SIGKILL` or OOM
termination, closes the descriptor and releases the kernel lock, so do not
remove the regular lock file during normal operation.

Every Git subprocess is noninteractive and receives a sanitized environment.
Local operations have a 30-second deadline and pushes have a two-minute
deadline; an earlier command, HTTP-request, or shutdown deadline still wins.
Cancellation terminates the Git process group, including SSH or credential
children. Lore disables repository hooks, filesystem monitors, signing,
automatic maintenance, editors, pagers, prompts, and askpass programs. It also
rejects managed or staged paths with an active Git `filter` attribute before
running commands such as status or add that could execute the filter. Use an
unlocked SSH deploy key or agent, or a preconfigured noninteractive HTTPS
credential helper, for pushes.

## `init`

```text
lore init [PATH] [--no-git] [--json]
```

Creates missing directories and baseline files without overwriting existing files. It is idempotent for an initialized repository. Unless `--no-git` is used, it creates a `main` Git repository when needed. If Git identity is available, a new repository receives:

```text
init: create Lore knowledge repository
```

Missing Git identity is a successful initialization with an actionable warning.

## `preflight`

```text
lore --repo PATH preflight [--sync] [--deep] [--branch NAME] [--json]
```

Runs the session-start safety workflow as one lock-protected operation. It
checks the expected branch (default `main`), the complete tracked and untracked
worktree, the recovery journal, and whether any actor owns a previewed
transaction. A blocker returns a structured result with `ready: false` and
exit code 4; preflight never stashes, resets, merges divergent history, or
discards a transaction.

`--sync` then fetches exactly the configured `git.remote` branch once. Lore
classifies the local clone as synchronized, behind, ahead, or diverged. It
fast-forwards only the strictly-behind case, using the remote-tracking ref from
that fetch rather than invoking `git pull` or performing a second network
request. Ahead or diverged history fails closed for explicit operator
reconciliation.

After synchronization, preflight checks the disposable index. It builds a
missing index and updates a stale compatible index, with canonical lint first,
then performs full verification when HEAD changed. When HEAD is unchanged and
the compatible index already certifies that exact clean Git snapshot, the fast
path uses lightweight index verification and does not repeat a full repository
lint. `--deep` forces lint and full index verification for an explicit audit.
Corrupt, incompatible, uncertified, or busy index state blocks rather than
being cleared automatically.

JSON includes stable blocker codes, initial ahead/behind counts, any dirty
paths, index action and final status, whether a fast-forward occurred, and
per-stage timing. Use this at the beginning of a normal synchronized local
agent session:

```bash
lore --repo . preflight --sync --json
```

For an intentionally local-only repository with no configured remote, omit
`--sync`. Local safety, lint, and index reconciliation still run:

```bash
lore --repo . preflight --json
```

## `capture`

```text
lore capture \
  --kind TOKEN \
  --origin TOKEN \
  --sensitivity normal|sensitive|local-only \
  [--origin-ref STRING] \
  [--tag STRING ...] \
  [--text STRING | --file PATH] \
  [--allow-empty] \
  [--no-commit] \
  [--push | --no-push] \
  [--json]
```

Sensitivity is required for every capture; Lore neither infers a classification
nor defaults to `normal`. When neither `--text` nor `--file` is present, capture
reads non-terminal stdin. Input is bounded by `capture.max_bytes` and must be
valid UTF-8. Prefer stdin to `--text` for private material.

Capture validates metadata before acquiring the repository write lock, then
publishes one no-clobber source file.

Auto-commit uses a path-limited Git commit:

```text
capture: <kind> <source-id>
```

Unrelated staged and working-tree changes remain untouched. `--no-commit` disables commit and push. Push overrides select whether to push the current branch to the configured remote.

The source remains present after every later Git failure. A required push failure reports that its commit is safe locally.

When a compatible index already exists and `index.auto_refresh_existing` is
enabled, capture attempts an index update only after source write, configured
commit, and push handling complete. Refresh failure is a warning and cannot
undo the source or local Git commit. Capture never creates an index.

### Write-lock upgrade compatibility

The first post-v0.4 writer converts the old `.lore/write.lock/` directory to
the persistent regular lock file only after the old directory disappears. A
running v0.4 writer can therefore finish and release normally while the new
writer waits. If the legacy directory remains after the bounded wait, Lore
fails closed and reports `legacy_lock: true` plus manual recovery guidance.
Stop all v0.4 writers, verify the recorded owner has exited, remove only that
legacy lock directory, and retry.

Once the regular lock file exists, v0.4 writers fail closed because their
directory creation cannot succeed. This makes the writer upgrade intentionally
one-way. To deliberately downgrade, stop every Lore writer, verify none holds
the lock, remove the regular `.lore/write.lock` file, and only then start v0.4.

## `search`

```text
lore search QUERY... \
  [--scope all|pages|sources] \
  [--kind TOKEN] \
  [--backend auto|index|filesystem] \
  [--matching auto|lexical|fuzzy] \
  [--include-sensitivity normal|sensitive|local-only ...] \
  [--limit N] \
  [--json]
```

Defaults are `scope=all`, `matching=auto`, and `limit=10`; maximum limit is
100. Query terms split on non-letter/non-number boundaries using Unicode-aware
lowercasing.

The explainable scorer keeps exact title and alias phrases dominant, then
scores exact title, alias, tag, body, and kind tokens plus tag/body phrases.
Body occurrence credit is bounded. Corpus-aware rarity credit favors exact
terms found in fewer authorized, already-filtered documents; distinct-term and
complete-query coverage bonuses favor documents that cover more of a
multi-term query. Results with no matched terms are omitted. Ordering is score
descending and path ascending. No recency boost, stemming, or synonym
expansion is used.

Matching mode is independent of the storage backend:

- `auto` is the default. It keeps exact lexical matching and typo-expands only
  out-of-vocabulary query terms of 6–24 Unicode characters.
- `lexical` disables fuzzy expansion and preserves strict exact-token behavior.
- `fuzzy` keeps exact matches and also expands every eligible term of 4–24
  characters. It is useful for deliberate maximum-recall searches.

Fuzzy matching uses bounded Unicode Damerau-Levenshtein distance: one edit for
4–7-rune terms and up to two edits for longer terms, subject to a minimum
similarity of 75% for terms below eight runes and 80% for longer terms. Auto
considers at most the eight longest eligible query terms and warns when it
truncates; explicit fuzzy rejects a broader request. At most four corrections
per term are considered. Exact scoring remains stronger, fuzzy phrase bonuses do not exist,
and each query term contributes at most once per document. Automatic mode
falls back to exact results with a warning if its authorized filtered
vocabulary exceeds the 100,000-term work bound; explicit `fuzzy` fails clearly
instead of returning a partial fuzzy search.

`auto` is the configuration default. It uses a fresh compatible derived index
only when indexed candidate generation preserves filesystem behavior;
otherwise it falls back with a warning. `index` refuses missing, stale,
corrupt, incompatible, or unsuitable state instead of silently returning old
data. `filesystem` reads Markdown directly.

All local sensitivities are included by default. Repeating
`--include-sensitivity` constructs a narrower explicit request policy, and
filtering occurs before index rows are returned to the core scorer.

Each result contains rank, score, path, URI, ID, title, kind, line range,
bounded snippet, and whole-file SHA-256 revision. A result reached through
fuzzy evidence adds deterministic `fuzzy_matches` entries containing the
query term, document term, and edit distance. Search JSON also contains
`backend`, `backend_requested`, `matching`, `fuzzy_expanded`, and `index_state`.
Oversized documents are skipped with warnings. Search never mutates canonical
knowledge.

## `read`

```text
lore read REFERENCE [--lines START:END] [--json]
```

Reference priority is exact path, ID, stem, page title, then page alias. A rule with multiple matches returns exit code 4 and sorted candidates. Absolute paths, `..` traversal, and symlink escape are rejected.

Line numbers are one-indexed and inclusive. An end beyond EOF is clamped; malformed, reversed, non-positive, or start-beyond-EOF ranges are rejected. Without a range, content is returned exactly.

## `references`

```text
lore references PAGE_REFERENCE [--json]
```

Resolves an existing synthesized page and inventories references visible to
the caller. Results separate live backlinks from other synthesized pages,
historical links in immutable source bodies, and source `integrated_into`
records. Each entry includes its current path, ID, revision, and relevant line;
link entries also include the exact destination.

Run this before changing a page path or ID, consolidating pages, or deleting a
page. Repair every live page backlink in the same transaction. Historical
source-body links are evidence and are neither rewritten nor required to keep
resolving. Source integration IDs are an additive historical ledger; add a
successor ID when useful rather than removing the old one. A newly supplied ID
must resolve after the proposed transaction; an existing ID may outlive its
page and is retained without being resubmitted.

## `lint`

```text
lore lint [--json]
```

Lint checks:

- `lore.yaml` presence and strict schema;
- required directories and Git ignore state;
- managed regular-file, symlink, size, and UTF-8 rules;
- frontmatter presence, types, tokens, dates, enums, and IDs;
- globally duplicate IDs and ambiguous page titles/aliases;
- exact source body hashes, source filename metadata, and UTC date partitions;
- inline and reference-style relative Markdown link existence and repository containment for synthesized pages;
- uncommitted source changes and detached Git HEAD warnings;
- stale preview warnings and active or malformed recovery-journal state.
- warnings for an existing stale, corrupt, incompatible, busy, or uncertified
  derived index;
- derived-index Git tracking, symlink, and restrictive-permission checks.

An absent index is not a finding. Derived warnings never make otherwise-valid
canonical Markdown invalid. Errors return 1. Warnings return 0 when no errors
exist. Git checks never contact a remote.

## `preview`

```text
lore preview [--input PATH|-] [--json]
```

Input defaults to non-terminal stdin. The strict request object requires
`schema_version`, `message`, and `operations`; unknown fields fail. Requests
are limited to 16 MiB and 50 unique-path operations.

Supported operations are:

- `create_page`: `op`, direct `pages/*.md` path, and complete `content`;
- `update_page`: the same plus the current whole-file `expected_revision`;
- `patch_page`: `op`, an existing direct `pages/*.md` path, its current
  whole-file `expected_revision`, and 1–50 exact `{old,new}` replacements;
- `delete_page`: `op`, an existing direct `pages/*.md` path, and its current
  whole-file `expected_revision`;
- `mark_source_integrated`: source `path`, `expected_revision`, and 1–50 unique
  `page_ids`;
- `set_source_sensitivity`: source `path`, `expected_revision`, and the new
  `sensitivity`. A change to a less restrictive classification also requires
  `allow_downgrade: true`.

Messages are one line, 1–160 UTF-8 bytes, contain no ASCII controls, and begin
with `integrate:`, `create:`, `update:`, `correct:`, `archive:`, or
`maintenance:`. Git retains the message permanently; do not include raw private
text, medical detail, credentials, or unnecessary sensitive information.

Preview requires a named Git branch and an existing commit. It rejects dirty
target paths, stale revisions, unsafe paths, an active recovery journal, and
page metadata violations. A page body change must set `updated` to at least the
current UTC calendar date. It never mutates the working tree, index, refs, or
history. Instead it overlays the exact effective bytes in memory, runs full
lint, and generates an uncolored unified diff with `a/` and `b/` paths.
For `patch_page`, every nonempty `old` block must occur exactly once in the
original page and replacement ranges must not overlap. All matches are made
against the original revision, not the result of earlier replacements. Include
the exact old and new `updated` lines when advancing the required date. A
missing, repeated, or overlapping block fails safely without including the
block's content in the error. Prefer this operation for small localized edits
and `update_page` for substantial rewrites; both still produce and validate the
complete prospective page and full diff.
Revision-guarded page updates may change a page ID; only `created` remains
immutable. Prospective lint prevents deletion or movement from leaving a broken
link in another synthesized page. A path move is therefore composed in one
request from a replacement create, backlink updates, optional additive source
integration, and deletion of the old path.
CLI previews belong to the `local-cli` actor and must be inspected, discarded,
or committed through CLI. MCP principals are distinct actors; switching
interfaces requires a fresh preview.
Source-sensitivity operations preserve the exact body and `raw_sha256`, retain
unrelated frontmatter, and pass through the same authorization, preview,
digest, lint, commit, and recovery checks as page operations.

Successful previews persist private mode-`0700`/`0600` artifacts beneath
`.lore/transactions/tx_<ULID>/` and return a digest of immutable
`proposal.json`. Lint errors return exit code 1 with the prospective diff and
findings but no committable transaction ID/digest pair.

## `commit`

```text
lore commit TRANSACTION_ID \
  --preview-digest sha256:... \
  [--push | --no-push] \
  [--json]
```

The digest is mandatory and compared in constant time. Commit re-reads and
hash-verifies the proposal, diff, lint, and every present-result content
artifact, then requires the exact preview branch, HEAD, target
existence/revisions, and clean target status. It reruns prospective lint and
regenerates the exact diff before any write.

Lore flushes an exact-original recovery journal before file application. After
verified atomic publication, it lints the real tree, commits only transaction
paths, and proves that the commit contains every and only those paths with the
proposed bytes or proposed absence. Unrelated staged and unstaged state remains
unchanged.

Changed preconditions return exit code 4; there is no force, merge,
ignore-revision, or skip-lint option. Read current state and preview again.
A repeated successful commit returns the original hash with
`already_committed: true`.

`--push`/`--no-push` override `git.auto_push_transactions`. Optional push
failure is a success warning. With `git.require_push: true`, failure returns 3
and states that the canonical commit is safe locally. A successful local commit
is never reset because push or derived-state maintenance fails.

After push handling, commit best-effort updates an already-existing compatible
index when configured. It never creates an index, and refresh failure is only a
warning.

## `transaction list`

```text
lore transaction list \
  [--status previewed|committed|discarded|failed|recovery_required] \
  [--limit N] \
  [--json]
```

The default limit is 20 and maximum is 200. Results are newest transaction ID
first and contain metadata only.

## `transaction show`

```text
lore transaction show TRANSACTION_ID [--diff] [--json]
```

Show verifies available artifacts and returns proposal metadata, lifecycle
state, hashes, and lint summary. `--diff` includes the full exact diff when it
still exists.

## `transaction discard`

```text
lore transaction discard TRANSACTION_ID [--json]
```

Only previewed or failed transactions may be discarded. The operation is
idempotent and blocked by active recovery. It deletes resulting content, diff,
and full lint payloads while retaining proposal/state receipt metadata and a
lint summary. Committed transactions cannot be discarded.

## `transaction prune`

```text
lore transaction prune \
  --older-than AGE \
  [--limit N] \
  [--dry-run] \
  [--json]
```

Prune is a local-only, explicit compaction command. `AGE` is a positive whole
number followed by `h`, `d`, or `w`, such as `24h`, `30d`, or `4w`. The cutoff
is computed once in UTC from the command clock. A committed transaction is
eligible when its immutable `committed_at` is at or before that cutoff;
subsequent push-related `updated_at` changes do not postpone retention.

Only committed transactions are eligible. Discarded transactions are already
compacted; failed transactions must be deliberately discarded; and previewed,
applying, or recovery-required work is never pruned. The default limit is 100,
the maximum is 1,000, and oldest commits are selected first with transaction ID
as the deterministic tie-breaker.

The operation holds the repository write lock and refuses while any recovery
journal is active. Before removal, it verifies every selected transaction,
requires its recorded commit to remain reachable from a local branch, remote
tracking ref, or tag, and proves the exact changed-path set and every committed
present-result blob hash or absent deletion. All selected transactions pass
preflight before the first payload is removed, and each is revalidated
immediately before compaction. These Git
checks run while the write lock is held, so schedule pruning while writers are
idle and start with a small `--limit` on constrained deployments.

Pruning retains `proposal.json` and `state.json` and adds a private
`retention.json` receipt. The receipt binds the transaction and preview digest
to an exact, sorted payload manifest and advances durably from `pruning` to
`pruned`. Lore then removes only the listed, hash-verified regular files:
`content/*.md`, `diff.patch`, and `lint.json`. A canceled or interrupted
operation remains valid and resumes idempotently on the next eligible prune.
Local list/show and repeated commit remain meaningful; compacted receipts are
not exposed through MCP because their content is no longer available for
sensitivity authorization.

`--dry-run` uses the same lock, recovery checks, integrity validation, Git
proof, cutoff, ordering, and limit without writing a retention receipt or
removing a file. JSON reports the exact cutoff, eligible/selected/remaining
counts, already-pruned receipts, reclaimable files/bytes, and deterministic
per-transaction details. Live output separately reports files and logical
bytes removed by that invocation.

Pruning is disk and retention hygiene, not secure erasure. It does not rewrite
Git, expire objects or reflogs, alter remotes, clean backups or snapshots, or
guarantee physical media erasure. Lore has no automatic retention
configuration or bundled timer.

## `recover`

```text
lore recover [--json]
lore recover --rollback [--json]
lore recover --finalize [--json]
```

Without an action, reports the active journal phase and exact recommended
command. `--rollback` is available before a transaction commit: it preflights
all target revisions, restores exact originals or removes Lore-created files,
reruns lint, marks the transaction failed, and removes the journal. It refuses
without changing files when an unexpected edit exists.

`--finalize` modifies no canonical content. It verifies an exact direct child
of the preview base commit on the preview branch, the complete changed-path
set, and every resulting blob SHA-256 or absent deletion before recording the
committed state.
Lore never automatically resumes an interrupted apply. See
[recovery.md](recovery.md).

## `index`

```text
lore index build [--force] [--json]
lore index update [--json]
lore index status [--verify] [--json]
lore index clear [--json]
```

`build` requires valid canonical documents and clean managed Git paths. It
scans current Markdown, builds and fully verifies a private temporary database,
then atomically installs it. A current compatible index requires `--force` for
replacement; failed construction preserves the prior index.

`update` requires an existing compatible index and the same canonical/Git
preconditions. It performs deterministic add/update/delete reconciliation and
full verification in one SQL transaction.

`status` is lightweight by default and reports `missing`, `fresh`, `stale`,
`uncertified`, `building`, `corrupt`, or `incompatible`. `--verify` adds full
SQLite/FTS consistency and secure-delete checks, plus canonical manifest
comparison where Git cannot certify freshness. Corrupt and incompatible status
return exit code 3.

`clear` acquires the exclusive derived-index operation lock and removes only
known index, WAL, shared-memory, and temporary-build files. It retains the
repository identity and is idempotent.

See [index.md](index.md) for storage, fallback, locking, recovery, and benchmark
details.

## `recent`

```text
lore recent [--limit N] [--all] [--json]
```

The default limit is 20 and maximum is 200. A Git repository is required. Default history is path-limited to `pages/` and `sources/`; `--all` removes the path filter. JSON contains full commit hashes, UTC commit timestamps, author fields, and subjects.

## `mcp`

```text
lore mcp stdio --repo PATH \
  [--profile local-full|local-query] \
  [--log-format text|json]

lore [--json] mcp check-config [--config PATH]
lore mcp serve [--config PATH]
```

`stdio` serves exactly one repository over stdin/stdout. Its default profile is
`local-full`; `local-query` exposes only authorized search, read, and resource
operations. The profile is selected by the trusted process launcher and cannot
be changed by an MCP request. Protocol frames are the only stdout output.

`check-config` strictly parses the external HTTP server configuration, resolves
the repository, validates the bind/network policy, and loads and verifies every
token file. The default path is `/etc/lore/mcp.yaml`. JSON output reports only
status, listen address, endpoint, and principal count; it never reports token
material.

`serve` loads the same configuration and runs stateless Streamable HTTP. It
independently bearer-authenticates every MCP request, filters discovery by the
matched principal, and handles SIGINT/SIGTERM with bounded graceful shutdown.
HTTP configuration owns the repository path; combining `--repo` with
`check-config` or `serve` is rejected.

See [mcp.md](mcp.md) for tools, resources, current Codex/Claude Code
registration, permissions, sensitivities, and troubleshooting. See
[configuration.md](configuration.md) and [deployment.md](deployment.md) before
running the HTTP transport.

## `version`

```text
lore version [--json]
```

Human output:

```text
lore <version> (<commit>, <build-date>)
```

Build variables are injectable:

```bash
go build -ldflags \
  "-X lore/internal/version.Version=0.4.0 \
   -X lore/internal/version.Commit=$(git rev-parse HEAD) \
   -X lore/internal/version.BuildDate=2026-07-29T00:00:00Z" \
  ./cmd/lore
```
