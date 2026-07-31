# Lore derived search index

Lore can keep a local SQLite FTS5 index beside a knowledge repository.
The index accelerates candidate discovery; Markdown and Git remain authoritative.
Deleting every index file loses no canonical knowledge.

## Normal use

The default search backend is `auto`:

```bash
lore --repo PATH search deployment
```

When a fresh compatible index exists and the query shape preserves Lore's
lexical semantics, `auto` uses it. Otherwise it searches the Markdown files and
returns the same public ranking, snippets, line numbers, revisions, and result
ordering. JSON responses identify `backend`, `backend_requested`, and
`index_state`.

Build an index when a repository becomes large enough to benefit:

```bash
lore --repo PATH index build
lore --repo PATH index status --verify
```

Capture and transaction commit update an existing compatible index on a
best-effort basis after canonical durability and push handling. They never
create an index automatically. A refresh failure never rolls back Markdown or
a Git commit; it returns a warning directing you to `lore index update`.

## Lifecycle commands

```text
lore index build [--force] [--json]
lore index update [--json]
lore index status [--verify] [--json]
lore index clear [--json]
```

- `build` scans every canonical document, builds one private temporary
  database, verifies it, and atomically installs it. A current compatible index
  requires `--force` for full replacement.
- `update` performs deterministic add/update/delete reconciliation in one SQL
  transaction and verifies the result before commit.
- `status` performs a lightweight freshness check. `--verify` adds SQLite
  integrity, FTS consistency, secure-delete, and non-Git manifest checks.
- `clear` removes only known derived index files. It is safe and idempotent.

In a Git repository, build and update require clean `pages/` and `sources/`.
Unrelated working-tree changes are permitted. A non-Git repository can be
indexed, but its state is `uncertified`; explicit indexed search compares the
complete canonical manifest before returning data.

## Search backends and access policy

```bash
lore search QUERY --backend auto
lore search QUERY --backend index
lore search QUERY --backend filesystem
lore search QUERY --include-sensitivity normal
```

- `auto` safely falls back for a missing, stale, corrupt, incompatible, busy,
  uncertified, or unsuitable index.
- `index` requires a usable index and returns an error rather than silently
  using stale data. In non-Git repositories it performs a manifest comparison.
- `filesystem` bypasses the index.

One- and two-character terms, punctuation-only input, and other shapes for
which FTS cannot guarantee lexical parity use the filesystem in `auto` mode.
User text is tokenized into quoted FTS terms; raw query text is never SQL or FTS
control syntax.

All sensitivity values may exist in the local index. The local CLI includes all
three by default. Repeat `--include-sensitivity` to construct a narrower
request policy before candidate rows leave the index package.

The public Go ranker is shared by both backends. Indexed search obtains the
size of the authorized, filtered corpus and exact per-query-token document
frequencies from a derived term table populated with Lore's Unicode tokenizer.
That table deliberately does not use FTS diacritic normalization. `kind` is an
FTS candidate field, so a kind-only match is discoverable by both backends.
These details preserve public scores and ordering even when the FTS candidate
set is bounded.

Search matching is separate from backend selection. Default `matching=auto`
expands only eligible out-of-vocabulary terms; `lexical` disables expansion and
`fuzzy` expands all eligible terms. Indexed expansion reads only exact terms
within the possible Unicode-rune-length range for the supported edit distance,
then applies the same bounded Damerau-Levenshtein implementation and scoring
used by filesystem search. This exhaustive length-bounded approach does not
depend on a trigram heuristic that could silently omit a valid correction.
Scope, kind, tag, path, and sensitivity filters are applied before vocabulary
statistics or corrections are returned from the index package.

## Files, identity, and permissions

Known derived files are:

```text
.lore/index.sqlite
.lore/index.sqlite-wal
.lore/index.sqlite-shm
.lore/index.build.*.sqlite
.lore/index.operation.lock
.lore/repository-id
```

`.lore/` is mode `0700` and index files are mode `0600` where POSIX modes are
available. Generated Git ignore rules cover all index files. Lint warns about a
tracked index, a symlink, open permissions, or unhealthy existing index without
making otherwise-valid canonical Markdown invalid.

An index records a stable repository identity. Git repositories use the
canonical Git common directory plus sorted root commits. A non-Git repository,
or Git repository without a commit, uses a private generated UUID under
`.lore/repository-id`. Copying an index to another repository makes it
incompatible.

The installed index uses WAL so readers can continue during incremental
updates. Full builds use a self-contained temporary database and an exclusive
operation lock; replacement occurs only after integrity and FTS verification.
The supported and tested operation-lock implementation is Linux `flock`.

## State and troubleshooting

| State | Meaning | Action |
|---|---|---|
| `missing` | No installed index | Build one only if desired |
| `fresh` | Git identity, branch, HEAD, and managed tree match | None |
| `stale` | Canonical snapshot or recovery state changed | Commit managed edits, then `index update` |
| `uncertified` | Git cannot certify freshness | Use explicit search's manifest check or rebuild/update |
| `building` | Exclusive work or an incomplete temporary build exists | Let the operation finish; inspect before clearing |
| `corrupt` | SQLite, metadata, or verification failed | `index clear`, then `index build` |
| `incompatible` | Schema or repository identity differs | `index clear`, then `index build` |

Recommended diagnosis:

```bash
lore --repo PATH lint
lore --repo PATH index status --verify
lore --repo PATH index update
```

If update reports incompatibility or corruption:

```bash
lore --repo PATH index clear
lore --repo PATH index build
```

Never edit `.lore/index.sqlite` with SQL. It is not a repair surface, and an
in-place schema migration is intentionally unsupported in v0.4.

The current derived index application schema is version `3`. It retains
indexed `kind` candidates and exact lexical term statistics and adds the exact
Unicode rune length needed for bounded fuzzy vocabulary lookup. A schema
version `1` or `2` index is safely reported as `incompatible`; clear and
rebuild it. This does not change Lore's repository configuration or public
JSON schema version, which remain version `1`.

## Retrieval-quality evaluation

Search behavior has a deterministic repository-owned evaluation harness:

```bash
make eval-retrieval
```

The harness stages the synthetic Markdown corpus under `eval/retrieval/` in a
temporary directory, builds a disposable index, and runs every graded case
through both forced filesystem search and the production `auto` path. It
reports hit rates, reciprocal rank, recall, nDCG, zero-result cases,
authorization violations, and exact result parity. It then compares the full
report with `eval/retrieval/baseline.json`; an intentional search change must
be reviewed before replacing that baseline.

The suite is a regression and design instrument, not a claim about retrieval
quality in a user's real repository. Add sanitized cases derived from actual
agent failures as Lore is exercised. See `eval/retrieval/README.md` for the
case schema and baseline workflow.

## Reproducible build benchmark

The benchmark generates 10,000 medium Markdown documents outside the timed
section and performs a forced verified build:

```bash
go test -run '^$' -bench BenchmarkBuildGeneratedCorpus \
  -benchtime=1x -benchmem ./internal/index
```

On the release workstation (Linux/amd64, AMD Ryzen 9 9900X, Go 1.26.5), the
v0.3 release candidate produced:

```text
809817535 ns/op
179286976 B/op
2221907 allocs/op
2.223 index/text
```

This is a reproducibility reference, not a performance gate. Corpus shape,
filesystem, cache state, CPU, and SQLite version affect results.
