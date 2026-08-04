# Lore transaction recovery

Lore writes a durable journal before the first transaction file change. An
active journal lives at `.lore/recovery/active/`, contains exact originals or
explicit absence markers, and blocks capture, preview, commit, and transaction
discard or prune until it is resolved.

Lore never automatically resumes an interrupted apply. The two explicit
outcomes are exact rollback before a transaction Git commit or verified
finalization after that commit.

The MCP gateway deliberately exposes no recovery mutation tool. An active
journal keeps authorized search, read, resources, history, and inspection
available but blocks capture, preview, commit, and transaction discard or
prune. HTTP liveness remains healthy, while the principal-independent readiness
probe returns a generic `503` until recovery is resolved.
Resolve recovery from a trusted local CLI session.

## Inspect first

```bash
lore --repo PATH recover
lore --repo PATH recover --json
```

Status validates the journal and reports its transaction, base branch/commit,
changed paths, durable phase, any recorded commit, and one recommended action.
Do not delete `.lore/recovery/active` manually unless the journal itself is
irreparably damaged and the repository has been reviewed independently.

Journal phases mean:

| Phase | Durable fact |
|---|---|
| `prepared` | exact originals and absence markers are flushed; no apply phase began |
| `applying_files` | zero or more file renames may have completed |
| `files_applied` | all resulting files were published; real-tree lint and commit may or may not have run |
| `git_committed` | the exact transaction commit hash was durably recorded |
| `finalized` | transaction state was committed; only journal removal remains |

The narrow crash window after Git commit but before `git_committed` can leave a
`files_applied` journal. Status inspects Git direct children and blob hashes; if
the exact commit exists, it recommends finalize rather than rollback.

## Roll back

```bash
lore --repo PATH recover --rollback
```

Rollback is for pre-commit recovery. Before changing anything, Lore verifies
every target:

- an updated target must equal either its recorded original SHA-256 or recorded
  Lore result;
- a created target must be absent or equal the recorded Lore result;
- a deleted target must equal its recorded original or be absent as proposed;
- paths must still be contained, regular, and non-symlink.

Any unexpected edit returns conflict exit code 4, preserves that edit, retains
the journal, and marks the transaction `recovery_required`. Review the file and
journal before retrying.

After a successful preflight, Lore clears only transaction paths from the Git
index, restores exact originals in reverse order, removes exact Lore-created
files, recreates exact Lore-deleted files, runs lint, marks the transaction
`failed`, and removes the journal.
Unrelated staged and unstaged changes remain untouched.

Rollback refuses when Git already contains the exact transaction commit, even
if a crash prevented the journal phase from recording it. Use finalize.

## Finalize

```bash
lore --repo PATH recover --finalize
```

Finalize never modifies canonical Markdown or resets Git. It requires proof
that one direct child of the preview base commit:

- is reachable from the recorded preview branch;
- changes every and only the recorded paths;
- contains a blob at each present-result path whose SHA-256 equals the recorded
  result and no blob at each absent-result deletion path.

It then records the full commit hash and Git commit time in transaction state,
advances the journal through `git_committed` and `finalized` as necessary, and
removes the active journal. If proof is absent or ambiguous, finalize refuses.

## Damaged journals

`lore lint` reports a malformed active journal as an error and a valid active
journal as a warning that writes are blocked. A damaged journal is an
integrity incident: preserve a copy of `.lore/recovery/active`, compare the
working tree and Git history against the transaction artifacts, and restore
from trusted backup if exact state cannot be established.

Recovery artifacts can contain exact private document bytes. Keep them out of
support tickets, logs, MCP requests, client transcripts, and commit messages.

If the interrupted transaction was created through MCP, its owner remains the
configured principal ID. Finalization records the original transaction commit;
rollback marks that transaction failed. Rotating a token while preserving the
principal name preserves ownership. Removing or renaming the principal does
not make local CLI recovery unsafe, but the replacement principal cannot
inspect or commit the old actor-bound proposal through MCP.

## Derived-index recovery is separate

The SQLite search index is not part of canonical transaction recovery. An
active recovery journal makes an installed index stale, so automatic and
explicit indexed search will not use it.

After rollback or finalize, inspect and refresh an existing index:

```bash
lore --repo PATH index status --verify
lore --repo PATH index update
```

If status reports corruption or incompatibility, delete only the derived index
and rebuild:

```bash
lore --repo PATH index clear
lore --repo PATH index build
```

Index update/build failure cannot undo a recovered canonical tree or Git
commit. Never edit SQLite metadata to make a stale index appear fresh.
