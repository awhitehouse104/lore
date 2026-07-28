# Lore

Lore is a private, deterministic personal-knowledge layer backed by Markdown and Git. It preserves exact raw source material, finds and reads evidence, validates repository integrity, and applies reviewed synthesized-page transactions as recoverable, exact-path Git commits.

The Markdown knowledge repository is authoritative. Search results, runtime state, agent sessions, and future adapters are replaceable.

> Raw source material is append-oriented and preserved. Synthesized pages are mutable. Git records both.

Lore does not call an LLM, answer natural-language questions, maintain a database, generate pages, run a daemon, or expose a server. An agent such as Codex or Claude Code can use Lore as a deterministic tool, but is not part of Lore itself.

## Requirements and installation

Building requires Go 1.26 and Git. The installed Lore binary requires only Git at runtime.

```bash
make check
make test-race
make build
sudo install -m 0755 lore /usr/local/bin/lore
```

`make build` injects the current commit and UTC build time. Override its defaults for a release build:

```bash
make build VERSION=0.2.0 COMMIT="$(git rev-parse HEAD)" BUILD_DATE=2026-07-28T00:00:00Z
```

Development builds made with `go build ./cmd/lore` safely report `0.2.0-dev`, `unknown`, and `unknown`.

## Five-minute quickstart

Initialize a separate knowledge repository:

```bash
lore init "$HOME/lore-home"
```

Capture exact source bytes through stdin. Stdin is preferred for private material because `--text` values may appear in shell history or process listings.

```bash
printf '%s' 'Project Foo should remain deployable without Kubernetes.' \
  | lore --repo "$HOME/lore-home" capture \
      --kind user_statement \
      --origin codex \
      --json
```

Use the returned source ID or path with `read`, and search for lexical evidence:

```bash
lore --repo "$HOME/lore-home" search deploy Foo Kubernetes
lore --repo "$HOME/lore-home" read src_01ARZ3NDEKTSV4RRFFQ69G5FAV --lines 1:120
lore --repo "$HOME/lore-home" lint
lore --repo "$HOME/lore-home" recent --limit 20
```

For normal page maintenance, prepare a strict transaction request, preview its complete diff and prospective lint result, then commit the exact preview using its returned digest:

```bash
lore --repo "$HOME/lore-home" preview --input request.json --json
lore --repo "$HOME/lore-home" commit tx_01ARZ3NDEKTSV4RRFFQ69G5FAV \
  --preview-digest sha256:0123456789abcdef... --json
```

The request can create or update direct children of `pages/` and can mark existing sources as integrated. Preview never changes canonical files or Git. A changed branch, HEAD, target revision, target Git status, artifact, or digest is a conflict; re-read the current documents and make a new preview rather than forcing it.

## Commands

Every data-returning command supports `--json`, and every JSON response contains `"schema_version": 1`.

### Initialize

```bash
lore init [PATH] [--no-git] [--json]
```

`init` creates the repository contract without overwriting existing files. Unless `--no-git` is used, it initializes a `main` branch and makes an initial commit when Git author identity is available.

### Capture

```bash
printf '%s' 'exact bytes' | lore --repo PATH capture \
  --kind user_statement --origin codex --tag project-foo --json

lore --repo PATH capture --kind note --origin import \
  --file ./note.md --sensitivity sensitive --no-commit
```

`--text`, `--file`, and piped stdin are mutually exclusive input sources. Capture preserves input bytes without trimming, newline insertion, newline conversion, Unicode normalization, or Markdown escaping. Use `--allow-empty` for an intentional empty body. `--push` and `--no-push` override repository push configuration.

### Search

```bash
lore --repo PATH search 'Project Foo'
lore --repo PATH search deployment --scope pages --kind project --limit 25 --json
```

Search is deterministic, Unicode-aware lexical ranking over current Markdown. It returns evidence records with metadata, a best-line snippet, Lore URI, line range, score, and SHA-256 revision. It never synthesizes an answer or mutates the repository.

### Read

```bash
lore --repo PATH read pages/project-foo.md
lore --repo PATH read page_project_foo --lines 10:40 --json
```

A reference may be an exact managed path, document ID, filename stem, page title, or page alias, in that priority order. Ambiguous references fail with candidate paths. Human-mode content is written unchanged to stdout; its identifying header goes to stderr.

### Lint

```bash
lore --repo PATH lint
lore --repo PATH lint --json
```

Lint validates configuration, structure, UTF-8, frontmatter, global IDs, page-name ambiguity, source hashes and paths, relative Markdown links, and selected Git state. Findings are deterministic. Errors return exit code 1; warnings do not.

### Preview and commit

```bash
lore --repo PATH preview [--input PATH|-] [--json]
lore --repo PATH commit TRANSACTION_ID \
  --preview-digest sha256:... [--push | --no-push] [--json]
```

`preview` accepts a bounded schema-version-1 JSON request from a file or non-terminal stdin. It validates exact target revisions, constructs resulting bytes in memory, runs full lint over that prospective view, generates a full unified diff, and stores a private proposal under `.lore/transactions/`. A lint-invalid preview returns exit code 1 and is not persisted as committable.

`commit` revalidates every artifact and precondition, writes a durable recovery journal, applies exact bytes, lints the real tree, and creates one Git commit containing every and only the transaction paths. Unrelated staged and unstaged changes remain untouched. Repeating a successful commit is idempotent.

Commit subjects are retained in Git. Keep transaction `message` values short and descriptive; never put private source text, medical details, credentials, or other unnecessary sensitive content in them.

### Transaction inspection

```bash
lore --repo PATH transaction list [--status STATUS] [--limit N] [--json]
lore --repo PATH transaction show TRANSACTION_ID [--diff] [--json]
lore --repo PATH transaction discard TRANSACTION_ID [--json]
```

Inspection verifies stored hashes before returning metadata. Discard is allowed only for previewed or failed transactions; it removes content, diff, and full lint artifacts while retaining a metadata receipt and lint summary.

### Recovery

```bash
lore --repo PATH recover [--json]
lore --repo PATH recover --rollback [--json]
lore --repo PATH recover --finalize [--json]
```

An active recovery journal blocks all content writers. Plain `recover` reports its durable phase and exact recommended action. Rollback first proves that every target is either original or Lore-applied and never overwrites an unexpected edit. Finalize changes no canonical files: it proves the exact direct-child Git commit and its blob hashes, reconciles transaction state, and removes the journal. See [the recovery guide](docs/recovery.md).

### Recent

```bash
lore --repo PATH recent --limit 20
lore --repo PATH recent --all --json
```

By default, recent history includes only commits touching `pages/` or `sources/`. `--all` includes every repository commit. The command never contacts a remote.

### Version

```bash
lore version
lore version --json
```

## Sources and pages

Sources live at `sources/YYYY/MM/src_<ULID>-<kind>.md`. Their body is the exact captured input, and `raw_sha256` protects it against later modification. Source bodies are immutable by policy; corrections are new captures.

Pages are flat files under `pages/`. They are mutable synthesis maintained by a human or agent, with stable IDs and citations to sources. Git records page evolution. See [the data model](docs/data-model.md) for both schemas.

## Repository selection and configuration

Commands resolve the knowledge repository in this order:

1. `--repo PATH`
2. `LORE_REPO`
3. Walk upward from the current directory to the first `lore.yaml`
4. Otherwise fail with an actionable error

`lore init` instead uses its positional path, or the current directory.

The v0.2 configuration remains schema version 1 and strict:

```yaml
version: 1

git:
  auto_commit_captures: true
  auto_push_captures: false
  auto_push_transactions: false
  remote: origin
  require_push: false

capture:
  max_bytes: 4194304
```

Unknown configuration keys are rejected. Capture and transaction commits preserve unrelated staged or working-tree changes. A transaction push failure never rolls back its local commit. Optional push failure is a warning; when `require_push` is true, it returns exit code 3 while reporting that the canonical update is safely committed locally.

`auto_push_transactions` is optional and defaults to `false`. Adding it to an existing repository is understood by v0.2, but strict v0.1 binaries reject that new key. Leave it absent until every client is upgraded if temporary mixed-version operation is required.

## Backup and security

Back up both the current knowledge repository and its Git history. A remote can provide another copy, but remote hosting is a separate disclosure boundary and is not configured by `lore init`.

Lore does not encrypt data. Filesystem permissions and the Unix account are the primary access boundary. Git can retain deleted content indefinitely. Read [the security guide](docs/security.md) before storing sensitive material.

## v0.2 limitations

Lore v0.2 intentionally has no semantic/vector search, database, arbitrary file-write command, page delete/rename, source-body edit, automatic synthesis, URL fetching, import pipeline, MCP/HTTP server, web interface, secret storage, encryption, multi-user permissions, background process, or transaction pruning. `local-only` is metadata and is not enforced against clients or remotes.

Transaction artifacts are derived state and may contain synthesized page bytes and diffs until discarded; committed transaction artifacts have no automatic retention policy in v0.2. Markdown and Git remain authoritative.
