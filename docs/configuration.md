# Lore configuration

`lore.yaml` is strict YAML with schema version `1`. Unknown keys, multiple YAML
documents, invalid types, and out-of-range values fail before an operation
starts.

The complete v0.4 knowledge-repository configuration is:

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

index:
  backend: auto
  auto_refresh_existing: true
  candidate_multiplier: 20
  minimum_candidates: 200
  maximum_candidates: 2000
```

Omitted fields receive these defaults.

## Git

- `auto_commit_captures` creates one path-limited commit after a successful
  capture.
- `auto_push_captures` pushes a capture commit unless the command overrides it.
- `auto_push_transactions` pushes a transaction commit unless overridden.
- `remote` is a Git remote name. It cannot begin with `-` or contain whitespace
  or control characters.
- `require_push` makes a requested/configured push failure return exit code 3.
  The local canonical commit remains safe.

## Capture

`capture.max_bytes` is the maximum exact input size. It must be positive and no
greater than 67,108,864 bytes (64 MiB). The default is 4,194,304 bytes (4 MiB).

## Index

- `backend` is `auto`, `index`, or `filesystem` and supplies the default when
  search has no `--backend`.
- `auto_refresh_existing` enables best-effort update after successful capture
  and transaction writes. It never creates an index.
- `candidate_multiplier` multiplies the requested public result limit to form
  an internal FTS candidate bound.
- `minimum_candidates` and `maximum_candidates` clamp that bound.

Bounds are:

| Field | Minimum | Maximum |
|---|---:|---:|
| `candidate_multiplier` | 1 | 100 |
| `minimum_candidates` | 1 | 10,000 |
| `maximum_candidates` | 1 | 100,000 |

`maximum_candidates` must be at least `minimum_candidates`. Candidate settings
affect performance and whether a `candidate_limit_reached` warning appears;
they do not change Lore's public scorer.

## Compatibility

v0.4 retains repository config schema version `1`. Existing v0.2
configurations without an `index` block are valid and receive current defaults.
Because v0.2 parsing is strict, v0.2 binaries reject the new `index` block.
During mixed-version operation, omit the block; `auto` still falls back to
the filesystem when no index exists.

Similarly, v0.1 rejects `git.auto_push_transactions`. Omit fields unknown to
the oldest active client until every client is upgraded.

## External MCP server configuration

HTTP serving uses a separate strict schema-version-`1` YAML file, not
`lore.yaml`. Keeping it outside the knowledge repository prevents content
changes from granting network access or changing principals. The default path
is `/etc/lore/mcp.yaml`.

Complete example:

```yaml
version: 1

repo: /srv/lore/home
listen: 127.0.0.1:8787
endpoint: /mcp

transport:
  request_max_bytes: 8388608
  response_max_bytes: 8388608
  max_concurrent_requests: 8
  request_timeout: 60s
  shutdown_timeout: 15s
  allowed_origins:
    - https://client.example
  trust_forwarded_headers: false
  allow_plaintext_non_loopback: false

auth:
  tokens:
    - name: remote-reader
      token_file: /etc/lore/tokens/remote-reader
      permissions:
        - query
      sensitivities:
        - normal

logging:
  format: json
  level: info
  destination: stderr
```

Unknown and duplicate keys, multiple YAML documents, invalid types, and
out-of-range values are rejected. `repo` must resolve to a valid Lore
repository. The file may be group-readable when its deployment requires that;
unlike token files, it should contain no credentials.

Validate configuration and every referenced token before service startup:

```bash
lore mcp check-config --config /etc/lore/mcp.yaml
lore --json mcp check-config --config /etc/lore/mcp.yaml
```

### Listener and transport

- `listen` defaults to `127.0.0.1:8787` and must be an explicit IP and port.
  Hostnames, unspecified addresses such as `0.0.0.0`, and invalid ports are
  rejected.
- `endpoint` defaults to `/mcp`. It must be one clean absolute path without
  traversal, query, fragment, wildcard, percent escape, or collision with
  `/health/live` or `/health/ready`.
- request and response maxima default to 8 MiB and range from 1 KiB through
  64 MiB.
- `max_concurrent_requests` defaults to `8` and ranges from `1` through `64`.
- `request_timeout` defaults to `60s` and ranges from `1s` through `5m`.
- `shutdown_timeout` defaults to `15s` and ranges from `1s` through `2m`.
- `allowed_origins` contains exact HTTP(S) origins. Lore normalizes scheme,
  host, and effective port, then requires an exact match. Entries cannot have
  credentials, paths, queries, fragments, wildcards, whitespace, or controls.
  An empty list rejects requests that carry `Origin` while allowing ordinary
  non-browser MCP clients that omit it.
- `trust_forwarded_headers` must remain `false` in v0.4.
- `allow_plaintext_non_loopback` defaults to `false`. A non-loopback bind must
  be explicit and set it to `true`; do so only when an independently encrypted
  and access-controlled private transport supplies confidentiality.

The built-in server does not terminate TLS. See [deployment.md](deployment.md)
for the two supported network topologies.

### Bearer principals

`auth.tokens` must contain at least one principal. Each entry has:

- a unique lowercase `name` beginning with a letter and containing at most 64
  letters, digits, underscores, or hyphens;
- one clean absolute `token_file`;
- additive `permissions`: `query`, `capture`, `curate`, `inspect`, `history`;
- an allowlist of `sensitivities`: `normal` and/or `sensitive`.

HTTP principals cannot include `local-only`. Duplicate names, permissions,
sensitivities, or decoded token values are rejected.

A token file must be a non-symlink regular file with no group or other
permission bits. It contains exactly one hexadecimal or unpadded/padded
base64url token that decodes to 32–1,024 bytes, plus at most one final LF or
CRLF. Lore loads tokens at process start, stores only SHA-256 digests in runtime
auth records, and compares candidates in constant time. Restart after token
rotation.

### Logging

`logging.format` is `json` or `text`; `level` is `debug`, `info`, `warn`, or
`error`. The only v0.4 destination is `stderr`.

Audit events contain request ID, principal ID for authenticated calls,
transport, operation, outcome, duration, and safe aggregate metadata. They do
not contain token material, queries, source bodies, snippets, page content,
diffs, or unauthorized titles and paths. Keep surrounding proxy and service
logs at the same privacy standard.
