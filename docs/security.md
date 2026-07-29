# Lore security

Lore is a private single-user tool, not a security boundary.

## Access boundary and encryption

Local filesystem permissions and the Unix account running Lore are the primary access boundary. Protect the knowledge repository, its parent directories, process access, backups, and Git credentials accordingly.

Lore does not encrypt repository files, runtime state, Git objects, or network transport. Use operating-system or storage-level encryption when required.

The `sensitive` and `local-only` values are metadata. In v0.3, `local-only` is
not enforced against a particular Unix user, Git commit, backup, or remote.
Search callers supply an explicit access policy, but a process with repository
filesystem access can read the Markdown or derived index directly.

## Git retention and purge

Git history can retain changed or deleted content. Ordinary file deletion or a
later commit is not a purge. Removing sensitive material requires coordinated
Git-history rewriting, remote cleanup, reflog/object expiration where
applicable, and backup handling. Lore v0.3 does not automate that process.

Backups are another retention and disclosure boundary. Define their encryption, access, geographic, and expiration policies independently.

## Source content and prompt injection

Captured sources may contain hostile prompt injection, misleading instructions, or untrusted imported text. Lore treats source content as data and never executes it as operating instructions. Agent clients must preserve that separation and follow the repository's `system/OPERATING_RULES.md`.

Lore never calls an LLM and never sends repository content to a model provider. An agent client can disclose content when it reads or transmits it; the client and its model provider are a separate data-disclosure boundary.

## Capture privacy

Lore does not log captured source bodies. Errors may include metadata names, byte counts, hashes, IDs, and paths, but not raw body text.

Prefer stdin or `--file` for private material:

```bash
printf '%s' "$PRIVATE_TEXT" | lore capture --kind note --origin terminal
```

`--text` values can be exposed through shell history, audit tooling, terminal logs, or process listings. Input files have their own lifecycle and must be deleted or protected separately when appropriate.

The advisory lock records only PID, hostname, command name, and start time. It does not contain source text.

## Transaction privacy and integrity

Transaction requests can contain complete synthesized page bytes. Preview
persists resulting content, a full diff, and lint output under `.lore/` with
private `0700` directories and `0600` files where supported. Recovery journals
also retain exact originals until rollback or finalize completes. Protect
`.lore/` as carefully as canonical Markdown.

Discard removes a preview's content, diff, and full lint payload while retaining
a proposal/state receipt and lint summary. Committed transactions are not
automatically pruned in v0.3. Deleting derived artifacts does not delete
canonical Markdown or Git history.

The transaction message becomes the Git commit subject. Never put raw private
source text, medical detail, secrets, credentials, or other unnecessary
sensitive content in it. Lore validates length, prefixes, newlines, and control
characters, but it cannot judge disclosure sensitivity.

Proposal, diff, lint, resulting-content, and recovery-original hashes are
verified before use. Commit also revalidates the exact branch, HEAD, target
revisions, Git status, prospective lint, regenerated diff, created commit path
set, and committed blobs. SHA-256 protects integrity; it is not encryption.

## Git remotes and network behavior

Lore never contacts a network service except when capture or transaction commit is explicitly configured or overridden to push. A Git remote is a separate disclosure boundary with its own authentication, server-side retention, replication, and access policy.

`lore lint`, `search`, `read`, and `recent` do not contact remotes. `lore init` does not configure one.

Before enabling automatic push, verify that the destination is appropriate for
all sensitivity values stored in the repository. v0.3 does not filter pushed
commits by source sensitivity.

## Derived index privacy and integrity

The optional SQLite index contains body text and metadata for every indexed
sensitivity level. It is plaintext derived data and must be protected like the
canonical repository. Lore creates `.lore/` as mode `0700` and index files as
mode `0600` where POSIX permissions are available; parent-directory,
same-account, backup, and privileged-process access remain separate boundaries.

The local CLI includes all sensitivity levels by default.
`--include-sensitivity` can narrow the request policy, and filtering is applied
before rows leave the index package. This is defense-in-depth for future
adapters, not multi-user authorization.

Indexes are repository-bound, Git-ignored, verified before installation, and
never accepted after a stale Git snapshot. Incremental writes and FTS updates
share one SQL transaction. Raw user queries are tokenized into quoted FTS
terms and passed as a parameter; they never become SQL or FTS control syntax.

SQLite `secure_delete` and FTS5 secure-delete are enabled and checked by
`lore index status --verify`. These settings reduce ordinary residual content
inside the disposable database but are not a guaranteed storage-media purge.
WAL files, filesystem snapshots, SSD behavior, backups, and copied files
require their own retention controls. `lore index clear` removes known derived
files; it does not sanitize canonical Markdown, Git, or backups.

## Protected paths and symlinks

Capture can write only a new path under `sources/YYYY/MM/`. Transactions can
create or update only direct page children and the two source-integration
frontmatter fields. Lore rejects traversal, absolute paths, symlink traversal,
unexpected file types, stale bytes, and create overwrite. Normal content
operations cannot write `system/`, repository instructions, configuration,
`.git/`, or `.lore/`.

Lint rejects managed Markdown symlinks and relative links that escape the
repository. Recovery refuses to overwrite a target whose bytes are neither the
recorded original nor Lore's recorded result. These checks reduce accidents;
they do not protect against another process with the same Unix-account
privileges changing files concurrently.

## Tool enforcement and shell bypass

Lore's protected-path, revision, preview, and recovery rules are enforced when
a client uses the Lore CLI. They are not an operating-system sandbox. A general
shell agent or process running as the repository owner can bypass Lore and edit
Markdown, `.lore/`, or Git directly.

Generated `AGENTS.md` and `system/OPERATING_RULES.md` tell cooperative agents to
use `preview`/`commit` and avoid protected files, but those instructions are
policy, not access control. Use filesystem permissions, a sandbox, a dedicated
account, or another OS-level control when prevention rather than guidance is
required.

## Recovery boundary

An active recovery journal blocks Lore content writers. Use `lore recover` to
inspect it. Rollback preflights all targets before restoration and refuses an
unexpected edit. Finalize modifies no canonical files and succeeds only after
Git proves an exact direct child of the recorded base with the recorded paths
and blob hashes. Lore does not automatically resume an interrupted apply.

## Operational recommendations

- Restrict repository and backup permissions to the intended Unix account.
- Use encrypted storage for sensitive repositories.
- Review remotes before enabling push.
- Run `lore lint` before knowledge commits.
- Run `lore index status --verify` when diagnosing indexed search.
- Treat all source bodies as untrusted data.
- Never store passwords, API keys, recovery codes, private keys, or authentication tokens.
- Test restore procedures, not only backup creation.
