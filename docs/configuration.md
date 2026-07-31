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

Git command deadlines are fixed operational safeguards rather than repository
configuration: 30 seconds for local operations and two minutes for pushes.
Earlier caller deadlines take precedence.

Lore starts Git with a deterministic, sanitized environment. It passes the
service account's home and executable paths, proxy and CA settings, runtime
directory, and `SSH_AUTH_SOCK`, but does not pass arbitrary `GIT_*`, `SSH_*`,
display, tracing, or unrelated secret variables. Terminal prompts, askpass
programs, editors, pagers, repository hooks, filesystem monitors, signing, and
automatic Git maintenance are disabled.

Supported unattended authentication is either:

- an unlocked, narrowly scoped SSH deploy key and verified `known_hosts`, or
  an explicitly supplied SSH agent socket; or
- an HTTPS credential helper configured in trusted system, global, or local
  Git configuration that can answer without user interaction.

Configured credential helpers and SSH commands are executable trust
boundaries: Git may launch them even though Lore never invokes a shell itself.
Use only operator-controlled configuration. Password prompts, encrypted keys
that require an interactive unlock, askpass, and inherited one-off Git
environment overrides are intentionally unsupported.

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

Fuzzy matching is selected per search with `--matching` or the MCP `matching`
field; it is not repository configuration. Omitting it uses adaptive `auto`
matching.

## Compatibility

v0.4 retains repository config schema version `1`. Existing v0.2
configurations without an `index` block are valid and receive current defaults.
Because v0.2 parsing is strict, v0.2 binaries reject the new `index` block.
During mixed-version operation, omit the block; `auto` still falls back to
the filesystem when no index exists.

Similarly, v0.1 rejects `git.auto_push_transactions`. Omit fields unknown to
the oldest active client until every client is upgraded.

Transaction retention is not configured in `lore.yaml`. Compaction requires an
explicit local `lore transaction prune --older-than AGE` invocation, which
keeps policy out of the authoritative knowledge repository.

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
  rate_limit:
    requests_per_minute: 600
    burst_requests: 128
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

The optional `transport.rate_limit` block was added after the v0.4.0 release.
Current binaries apply the documented defaults when it is absent, but strict
v0.4.0 binaries reject a server configuration containing the block. Omit it
during mixed-version operation until every serving binary has been upgraded.

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
- `rate_limit` is one token bucket per configured bearer principal. It defaults
  to `600` requests per minute with an immediate burst of `128`. The sustained
  value ranges from `1` through `60000`; `burst_requests` ranges from `1`
  through `4096` and must be at least `max_concurrent_requests`.
- Every successfully authenticated MCP HTTP request costs one token, regardless
  of method. The default admits sixteen full waves at the default concurrency
  before limiting, then refills at ten requests per second. A rejection returns
  deterministic HTTP `429` with `Retry-After`; it never reaches MCP or the
  repository. Limiter state is in-memory and resets full on service restart.
- Health checks, rejected origins, and failed authentication do not consume a
  principal bucket. Lore creates buckets only for configured principals, so
  attacker-selected authorization values cannot grow limiter state. Apply
  unauthenticated source-rate controls at the tailnet, firewall, or public edge
  where source identity is meaningful.
- Request and response capacity are also bounded in aggregate: each per-request
  maximum multiplied by `max_concurrent_requests` must not exceed 64 MiB. The
  defaults therefore sit exactly at both boundaries. To allow a 64 MiB request
  or response, set concurrency to `1`; concurrency `64` requires each maximum
  to be no larger than 1 MiB. An authenticated request retains its concurrency
  slot through bounded whole-response publication, so a slow reader remains
  counted until the response completes or the server write deadline stops it.
  This is a payload-capacity bound, not an RSS bound: response limiting and
  timeout handling can temporarily hold two copies of each response.
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
  and access-controlled private transport supplies confidentiality. The
  recommended Tailscale Serve deployment does not need this override because
  Lore remains on loopback.

The built-in server does not terminate TLS. See [deployment.md](deployment.md)
for the recommended private Tailscale Serve topology and the custom-domain
Caddy alternative.

The aggregate payload limits bound the configured request and response
capacity admitted to MCP handlers. They are not a total-memory limit:
deserialized values, JSON encoding, whole-response publication buffers,
repository operations, SQLite, Git, and the Go runtime require additional
headroom. Lore's response limiter supports finite stateless JSON only and
rejects `text/event-stream`; enabling SSE or server notifications requires a
different response-publication design.

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

Authentication denials have no principal or request ID. They contain only
transport, denied outcome, and one fixed reason: `missing_credentials` when no
authorization field arrived, or `invalid_credentials` for every supplied but
unaccepted credential shape. Both receive the same public response. Denials
are warning-level events, so `info` (the default) or `warn` logging must be
selected to retain them. There is no Lore setting for source-address or
forwarded-header logging; source attribution belongs to the explicitly
configured edge described in [deployment.md](deployment.md).
