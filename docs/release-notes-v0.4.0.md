# Lore v0.4.0 release notes

Lore v0.4.0 adds a capability-scoped MCP gateway over local stdio and
authenticated stateless Streamable HTTP while preserving Markdown and Git as
the authoritative knowledge store.

## Included

- Official `github.com/modelcontextprotocol/go-sdk v1.7.0`, modern MCP
  `2026-07-28` discovery, and the SDK's legacy initialization compatibility
- Local `lore mcp stdio` with fixed `local-full` and `local-query` profiles
- Strict external HTTP configuration, protected token files, constant-time
  bearer matching, and independently authenticated requests
- Explicit `query`, `capture`, `curate`, `inspect`, and `history` permissions
- Principal sensitivity allowlists, not-found masking, actor-owned
  transactions, and commit-time reauthorization
- Defensive exclusion of `local-only` knowledge from every HTTP principal
- Bounded MCP search, read, recent, lint, index, capture, preview, commit, and
  transaction tools with deterministic schema-v1 structured content
- Principal-scoped durable capture/commit idempotency containing no source
  bodies or diffs
- Authorized page resources, canonical page/source resource templates,
  private zero-TTL responses, and post-commit resource refresh
- Exact support in `lore_read` for both search path URIs and ID-based resource
  URIs
- Exact origin policy, safe bind/plaintext defaults, request/response/
  concurrency/time limits, private caching headers, minimal health endpoints,
  and bounded graceful shutdown
- Metadata-only audit events, generic authentication denial, correlation IDs,
  and redacted panic recovery
- Prompt-injection, path/URI confusion, actor spoofing, idempotency,
  authorization, recovery, concurrency, shutdown, and seeded-secret tests
- Current Codex and Claude Code stdio/HTTP examples, permission and sensitivity
  reference, token rotation, troubleshooting, deployment topologies, and a
  hardened systemd example

No MCP operation exposes arbitrary shell, Git, SQL, or filesystem access. MCP
handlers call the same typed core services as the CLI.

## Compatibility and upgrade

- `lore.yaml` remains strict schema version `1`; existing v0.3 repositories
  require no migration.
- Canonical source and page Markdown formats are unchanged.
- Existing CLI JSON remains schema version `1`. Search results add
  `resource_uri`; existing `uri` behavior is retained.
- HTTP configuration is deliberately separate from the knowledge repository
  and also uses its own strict schema version `1`.
- The server targets modern MCP revision `2026-07-28` and retains the official
  SDK's legacy `2025-11-25` initialization path.
- The derived index remains optional, local, and rebuildable.

To add local query-only access:

```bash
codex mcp add lore -- \
  /usr/local/bin/lore mcp stdio \
  --repo /srv/lore/home --profile local-query
```

Read [mcp.md](mcp.md) before granting mutation permissions and
[deployment.md](deployment.md) before enabling HTTP.

## Security model

The trusted launcher or external server configuration chooses a principal;
tool arguments cannot override identity or grants. Discovery is filtered for
usability, and every invocation independently authorizes again. Unauthorized
direct reads are deliberately indistinguishable from nonexistent content.

Remote service tokens must be hexadecimal or base64url encodings of 32–1,024
decoded bytes in non-symlink mode-`0600` regular files. Lore loads them at
startup and retains only SHA-256 digests in authentication records. Restart is
required after rotation.

The built-in listener is plaintext. Loopback behind a TLS/authenticated reverse
proxy is recommended. Non-loopback serving requires an explicit exact IP and
an explicit override intended only for an independently encrypted,
access-controlled private tailnet. Public unauthenticated serving is
unsupported.

Retrieved Markdown remains untrusted evidence. Prompt-injection text cannot
change registered tools, permissions, principals, paths, preview digests, or
protected-file rules. Client/model transcripts are a separate disclosure
boundary.

## Client and protocol verification

The release candidate passed the complete workflow
`search → read → capture → search/read → preview → show → commit → read`, plus
a masked read attempt, in disposable repositories through:

- Codex CLI `0.145.0`, local stdio;
- Codex CLI `0.145.0`, authenticated Streamable HTTP;
- Claude Code `2.1.217` with Sonnet, local stdio;
- Claude Code `2.1.217` with Sonnet, authenticated Streamable HTTP.

The default Claude model reported the account's monthly spend limit; selecting
Sonnet completed both Claude workflows without changing Lore behavior.

Official MCP Inspector `1.0.1` CLI checks passed over both transports for tool
discovery, schemas, annotations, resource listing, private cache metadata, and
ID-resource-URI reads. That stable Inspector version emits its upstream v1
deprecation notice; v2 was still a release candidate on the verification date.

The exact Inspector forms were:

```bash
npx -y @modelcontextprotocol/inspector@1.0.1 --cli \
  /path/to/lore mcp stdio --repo /path/to/repo --profile local-full \
  --method tools/list

npx -y @modelcontextprotocol/inspector@1.0.1 --cli \
  https://lore.example/mcp --transport http \
  --header "Authorization: Bearer <test-token>" \
  --method tools/list
```

An installed-client smoke exposed that clients can pass search's new
ID-based `resource_uri` to `lore_read`; the initial candidate accepted that URI
only through MCP `resources/read`. The final candidate accepts it through both
interfaces and includes focused core and in-process protocol regression tests.

On Fedora systemd 259, the shipped unit parsed successfully apart from the
expected release-workstation warning that `/usr/local/bin/lore` was not
installed. Offline hardening analysis rated it `2.8 OK`. A disposable
systemd-managed user service started Lore, passed both health checks, handled
stop cleanly, and was auto-collected. The test harness had to omit `PrivateTmp`
because its binary and config intentionally lived under `/tmp`; the shipped
unit keeps `PrivateTmp=true` and uses `/usr/local`, `/etc`, `/srv`, and
`/var/lib`.

## Dependencies and verification

The new direct dependency is the official Go MCP SDK v1.7.0. Its compiled
transitive closure and exact license classifications are recorded in
[dependencies.md](dependencies.md).

Release-candidate verification on 2026-07-29 passed:

```text
gofmt -l .                         clean
go vet ./...                       passed
go test ./...                      passed
go test -race ./...                passed
go build ./cmd/lore                passed
CGO_ENABLED=0 go build ./cmd/lore  passed
go mod tidy -diff                  clean
go mod verify                      all modules verified
govulncheck ./...                  No vulnerabilities found
```

`govulncheck` v1.6.0 used the database updated
2026-07-27T20:14:16Z. Vulnerability data changes; rerun it for future builds.

## Deliberate boundaries

v0.4 does not add a server-side LLM, semantic/vector search, authoritative
database, arbitrary filesystem or shell tool, web UI, OAuth provider,
multi-repository routing, public unauthenticated listener, page delete/rename,
source-body edit, autonomous maintenance, encryption, secret manager, or
automatic transaction pruning.

Hosted ChatGPT support is not a release promise. Hosted reachability,
authentication, connector/plugin availability, and workspace policy change
independently of Lore. A tunnel, OAuth proxy, or future connector can be added
outside Lore without changing its canonical data format.
