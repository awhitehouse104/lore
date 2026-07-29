# Lore security

Lore is a private knowledge tool. Its MCP gateway enforces configured
capabilities and sensitivity policy, but it is not an operating-system sandbox
or a replacement for host, network, backup, client, and model-provider
controls.

## Access boundary and encryption

Local filesystem permissions and the Unix account running Lore are the primary access boundary. Protect the knowledge repository, its parent directories, process access, backups, and Git credentials accordingly.

Lore does not encrypt repository files, runtime state, Git objects, or network transport. Use operating-system or storage-level encryption when required.

The `sensitive` and `local-only` values remain metadata for a process with
direct filesystem access. The v0.4 MCP gateway enforces a principal's
sensitivity allowlist on search, direct read, resources, history, inspection,
capture, preview, transaction ownership, and commit-time reauthorization.
HTTP principals cannot receive `local-only`; this is enforced by strict config
validation and again by the core access policy. The local CLI and stdio
profiles run as the invoking Unix user and can access all sensitivities their
fixed profile permits.

## Git retention and purge

Git history can retain changed or deleted content. Ordinary file deletion or a
later commit is not a purge. Removing sensitive material requires coordinated
Git-history rewriting, remote cleanup, reflog/object expiration where
applicable, and backup handling. Lore v0.4 does not automate that process.

Backups are another retention and disclosure boundary. Define their encryption, access, geographic, and expiration policies independently.

## Source content and prompt injection

Captured sources may contain hostile prompt injection, misleading instructions, or untrusted imported text. Lore treats source content as data and never executes it as operating instructions. Agent clients must preserve that separation and follow the repository's `system/OPERATING_RULES.md`.

Lore never calls an LLM and never independently sends repository content to a
model provider. An MCP or CLI agent can disclose content when it reads or
transmits it; its transcript, logs, host, and model provider are separate
data-disclosure boundaries.

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
automatically pruned in v0.4. Deleting derived artifacts does not delete
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
all sensitivity values stored in the repository. v0.4 does not filter pushed
commits by source sensitivity.

## MCP authorization and network boundary

Local stdio profiles are fixed by the trusted process launcher:

- `local-query` grants `query`;
- `local-full` grants `query`, `capture`, `curate`, `inspect`, and `history`.

Both can access `normal`, `sensitive`, and `local-only` because they run
locally. A client cannot supply or override its principal, permissions, or
sensitivities in tool arguments.

HTTP principals come only from strict external configuration. Every request is
independently matched to exactly one protected bearer-token digest. Missing,
malformed, duplicated, unknown, and wrongly encoded authorization values share
the same public denial. Token files are non-symlink regular files with no group
or other permission bits; token bodies never enter configuration errors or
logs.

Tool discovery is permission-filtered, but authorization does not rely on
discovery: every invocation checks again. Unauthorized direct reads are
indistinguishable from nonexistent content. Transactions are actor-bound, and
commit rechecks current sensitivities. Capture and commit idempotency records
are principal-scoped and contain hashes plus minimal result metadata, never
captured bodies or diffs.

The built-in HTTP server is plaintext. Loopback is the safe default.
Non-loopback serving requires an explicit exact IP and an explicit override
intended only for an encrypted, access-controlled private tailnet. For broader
reachability, keep Lore on loopback behind a TLS/authenticated reverse proxy.
Unspecified binds and public unauthenticated serving are unsupported.

Origin enforcement uses exact normalized HTTP(S) origins and never trusts
forwarded headers. Request bytes, response bytes, concurrency, request time,
and graceful shutdown are bounded. MCP responses and resources use private
no-store/zero-TTL cache controls.

See [the MCP guide](mcp.md), [configuration reference](configuration.md), and
[deployment guide](deployment.md).

## Audit and error privacy

MCP audit events contain correlation ID, authenticated principal ID,
transport, operation, outcome, duration, and safe aggregate metadata.
Authentication denial uses a generic event without attempted token or identity
details. Panic recovery returns a redacted internal error and records no
request body.

Logs and externally mapped errors exclude bearer tokens, queries, source
bodies, page content, snippets, diffs, transaction artifact bodies, and
unauthorized titles or paths. The same standard must be maintained by reverse
proxies, systemd, client debug logs, shell histories, and model transcripts.
Do not enable HTTP body logging around Lore.

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
use bounded Lore operations and avoid protected files, but those instructions
are policy, not access control. MCP itself exposes no arbitrary shell, Git,
SQL, or filesystem tool. Still use filesystem permissions, a sandbox, a
dedicated account, or another OS-level control when prevention rather than
guidance is required.

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
- Use a query-only MCP principal unless mutation is required.
- Keep HTTP on loopback behind TLS or on an independently secured private
  tailnet.
- Rotate bearer tokens and restart the service after suspected disclosure.
- Run `lore lint` before knowledge commits.
- Run `lore index status --verify` when diagnosing indexed search.
- Treat all source bodies as untrusted data.
- Never store passwords, API keys, recovery codes, private keys, or authentication tokens.
- Test restore procedures, not only backup creation.
