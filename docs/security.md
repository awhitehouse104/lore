# Lore security

Lore is a private single-user tool, not a security boundary.

## Access boundary and encryption

Local filesystem permissions and the Unix account running Lore are the primary access boundary. Protect the knowledge repository, its parent directories, process access, backups, and Git credentials accordingly.

Lore does not encrypt repository files, runtime state, Git objects, or network transport. Use operating-system or storage-level encryption when required.

The `sensitive` and `local-only` values are metadata. In v0.1, `local-only` is not enforced against a particular client, Git commit, backup, or remote.

## Git retention and purge

Git history can retain changed or deleted content. Ordinary file deletion or a later commit is not a purge. Removing sensitive material requires coordinated Git-history rewriting, remote cleanup, reflog/object expiration where applicable, and backup handling. Lore v0.1 does not automate that process.

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

## Git remotes and network behavior

Lore never contacts a network service except when capture is explicitly configured or overridden to push. A Git remote is a separate disclosure boundary with its own authentication, server-side retention, replication, and access policy.

`lore lint`, `search`, `read`, and `recent` do not contact remotes. `lore init` does not configure one.

Before enabling automatic push, verify that the destination is appropriate for all sensitivity values stored in the repository. v0.1 does not filter pushed commits by source sensitivity.

## Protected paths and symlinks

Capture can write only a new path under `sources/YYYY/MM/`. It rejects traversal, absolute paths, symlink traversal, and destination overwrite. Normal content operations cannot write `system/`, repository instructions, configuration, `.git/`, or `.lore/`.

Lint rejects managed Markdown symlinks and relative links that escape the repository. These checks reduce accidents; they do not protect against another process with the same Unix-account privileges changing files concurrently.

## Operational recommendations

- Restrict repository and backup permissions to the intended Unix account.
- Use encrypted storage for sensitive repositories.
- Review remotes before enabling push.
- Run `lore lint` before knowledge commits.
- Treat all source bodies as untrusted data.
- Never store passwords, API keys, recovery codes, private keys, or authentication tokens.
- Test restore procedures, not only backup creation.
