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

## Repository write serialization

Operations that mutate canonical documents, transaction/recovery state, or
derived index files share a Linux `flock` on the private persistent regular
file `.lore/write.lock`. The kernel releases the lock when its descriptor
closes, including after `SIGKILL`, OOM termination, or process failure. Writers
retry with context-aware backoff for at most two seconds, then return a typed
conflict.

The lock file contains only schema version, PID, hostname, command name, and
start time. It never contains source text. Metadata is diagnostic and may be
unavailable during a race or malformed after external modification; kernel
lock ownership remains authoritative. Lore therefore does not infer safety
from PID liveness and never removes the regular lock file automatically.

An old v0.4 `.lore/write.lock/` directory is treated as a legacy lock and is
never broken automatically. If it persists after all v0.4 writers have
stopped, verify the recorded owner has exited before removing that directory.
Do not remove the regular post-v0.4 lock file during normal operation.

## Transaction privacy and integrity

Transaction requests can contain complete synthesized page bytes. Preview
persists resulting content, a full diff, and lint output under `.lore/` with
private `0700` directories and `0600` files where supported. Recovery journals
also retain exact originals until rollback or finalize completes. Protect
`.lore/` as carefully as canonical Markdown.

Discard removes a preview's content, diff, and full lint payload while retaining
a proposal/state receipt and lint summary. Explicit local transaction pruning
does the same for old committed payloads after proving that the exact commit is
still reachable and matches every recorded path and blob hash. A durable
retention receipt makes interrupted pruning resumable. Active recovery and all
noncommitted states are protected.

Pruning does not securely erase content. It does not delete canonical
Markdown, rewrite or expire Git history, alter remotes, remove backups or
snapshots, or guarantee physical-media erasure. There is no automatic
repository retention policy.

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

Git subprocesses run noninteractively with a sanitized environment and fixed
deadlines: 30 seconds locally and two minutes for push, bounded further by any
earlier caller deadline. Cancellation kills the Git process group so an SSH or
credential child cannot remain after Git exits.

Lore disables local repository hooks, filesystem-monitor commands, commit and
push signing, automatic maintenance and garbage collection, editors, pagers,
terminal prompts, and askpass programs. The external transport protocol is
disabled. Managed or staged paths with an active Git `filter` attribute are
rejected before status/add operations can invoke a clean or process filter.
These safeguards prevent an untrusted knowledge repository from gaining
execution through common Git extension points.

The sanitized environment retains only the values needed for normal local
operation and unattended SSH or HTTPS authentication, including home/path,
proxy and CA settings, runtime directory, and `SSH_AUTH_SOCK`. Git credential
helpers and configured SSH commands remain operator-controlled executable trust
boundaries. Configure only trusted helpers and commands; Lore does not make
arbitrary Git configuration safe. Server-side hooks are under the remote
operator's authority and are unaffected by local hardening.

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

The built-in HTTP server is plaintext. Loopback is the safe default. A private
Tailscale Serve edge is the recommended remote posture: tailnet policy limits
network reachability, Serve terminates HTTPS, and Lore still requires its own
bearer token. Caddy on loopback is the documented custom-domain alternative.
Neither proxy identity headers nor TLS replace Lore authorization.
Non-loopback serving requires an explicit exact IP and an explicit override
and is only an advanced fallback for an encrypted, access-controlled private
tailnet. Unspecified binds and public unauthenticated serving are unsupported.

Origin enforcement uses exact normalized HTTP(S) origins and never trusts
forwarded headers. Request bytes, response bytes, concurrency, request time,
per-principal request rate, and graceful shutdown are bounded. Authenticated
rate-limit state is fixed at one bucket per configured principal and cannot
grow from attacker-selected tokens. Health, origin denial, and authentication
denial do not consume those buckets; unauthenticated source-rate enforcement
belongs at the tailnet, firewall, or public edge. MCP responses and resources
use private no-store/zero-TTL cache controls.

Unauthenticated liveness is process-only. Readiness performs only a fixed set of
repository identity, required-directory, and active-recovery path checks; it
never scans content, invokes Git, or exposes the failing condition. Repository
degradation and active recovery share one generic `503` body. Detailed recovery
and lint diagnostics remain local operations.

HTTP page-resource metadata is enumerated only when `resources/list` is
requested and is not cached across stateless requests. This avoids making weak
mtime, TTL, or Git-state invalidation part of sensitivity enforcement. Exact
resource reads always resolve and authorize current Markdown independently of
the previously listed descriptor.

See [the MCP guide](mcp.md), [configuration reference](configuration.md), and
[deployment guide](deployment.md).

## Audit and error privacy

MCP audit events contain correlation ID, authenticated principal ID,
transport, operation, outcome, duration, and safe aggregate metadata.
Authentication denial uses a generic event with only `missing_credentials` or
`invalid_credentials`; the public response remains identical. It records no
direct or forwarded address, attempted token or identity, header, path, query,
or body. Panic recovery returns a redacted internal error and records no
request body.

Logs and externally mapped errors exclude bearer tokens, queries, source
bodies, page content, snippets, diffs, transaction artifact bodies, and
unauthorized titles or paths. The same standard must be maintained by reverse
proxies, systemd, client debug logs, shell histories, and model transcripts.
Do not enable HTTP body logging around Lore.

With Tailscale Serve or Caddy, Lore's peer is the loopback proxy. Source-IP
enforcement therefore belongs at the edge; banning from Lore audit events could
block the proxy itself. The optional Caddy observability example is deliberately
separate from the no-access-log default, deletes credentials and variable
request metadata, and has short bounded retention. Its source IP, timestamp,
and success/denial history remain personal metadata requiring an explicit
retention decision.

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

Fuzzy vocabulary discovery applies the caller's sensitivity, scope, kind, tag,
and path filters before computing document frequency or selecting corrections.
Consequently, an inaccessible unique term cannot become a visible fuzzy
correction or result. Expansion counts, term lengths, edit distance, and the
filtered vocabulary work set are bounded. When automatic expansion exceeds
that work bound Lore returns exact results with a warning; explicit fuzzy mode
fails rather than publishing an arbitrary partial correction set.

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
- Keep Lore on loopback behind Tailscale Serve by default; use the documented
  Caddy edge only when a custom-domain client cannot join the tailnet.
- Never expose Lore health paths through the remote edge.
- Rotate bearer tokens and restart the service after suspected disclosure.
- Run `lore lint` before knowledge commits.
- Run `lore index status --verify` when diagnosing indexed search.
- Treat all source bodies as untrusted data.
- Never store passwords, API keys, recovery codes, private keys, or authentication tokens.
- Test restore procedures, not only backup creation.
