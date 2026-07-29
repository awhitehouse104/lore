# Lore MCP gateway

Lore v0.4 exposes the same typed, deterministic operations as the CLI through
the official Go MCP SDK. It supports local stdio and stateless Streamable HTTP.
Markdown and Git remain authoritative; the gateway does not add an LLM,
database authority, arbitrary filesystem access, shell access, or autonomous
maintenance.

Retrieved Markdown is untrusted input. A source can contain instructions aimed
at an agent, but those words do not change Lore's registered tools,
authorization, paths, or protected-file rules. Clients must keep treating
retrieved content as evidence rather than operating instructions.

## Local stdio

Run one repository with a fixed launcher-selected profile:

```bash
lore mcp stdio --repo /srv/lore/home --profile local-full
lore mcp stdio --repo /srv/lore/home --profile local-query
```

`local-full` exposes every MCP permission and all sensitivities.
`local-query` exposes only search, read, and resources, but still permits all
local sensitivities. The profile is not a client tool argument. Stdio protocol
messages are the only stdout output; diagnostics use stderr.

### Codex

The current Codex configuration lives in `~/.codex/config.toml`, or in a
trusted project's `.codex/config.toml`:

```toml
[mcp_servers.lore]
command = "/usr/local/bin/lore"
args = ["mcp", "stdio", "--repo", "/srv/lore/home", "--profile", "local-full"]
default_tools_approval_mode = "writes"
```

Equivalent current CLI registration:

```bash
codex mcp add lore -- \
  /usr/local/bin/lore mcp stdio \
  --repo /srv/lore/home --profile local-full
```

`default_tools_approval_mode = "writes"` asks for approval on mutating tools.
Client confirmation is defense in depth: Lore independently authorizes every
call and revalidates every write.

### Claude Code

Current Claude Code registration requires `--` before the stdio command:

```bash
claude mcp add --transport stdio --scope local lore -- \
  /usr/local/bin/lore mcp stdio \
  --repo /srv/lore/home --profile local-full
```

`local` scope is private to the current project. Use `--scope user` only when
the same repository should be available from all of your projects. A
project-scoped `.mcp.json` is shareable and therefore should contain no
credentials:

```json
{
  "mcpServers": {
    "lore": {
      "type": "stdio",
      "command": "/usr/local/bin/lore",
      "args": [
        "mcp",
        "stdio",
        "--repo",
        "/srv/lore/home",
        "--profile",
        "local-full"
      ]
    }
  }
}
```

## Remote Streamable HTTP

Start the server only from a strict external configuration:

```bash
lore mcp check-config --config /etc/lore/mcp.yaml
lore mcp serve --config /etc/lore/mcp.yaml
```

The endpoint is stateless. Every request must carry its bearer token; there is
no login session, cookie, token minting, or OAuth flow in v0.4. Use a private
network or an authenticated TLS reverse proxy. Never expose Lore's plaintext
listener directly to a public network.

### Codex

Keep the token in the client process environment, not in TOML:

```bash
export LORE_MCP_TOKEN='replace-with-the-token-file-value'
codex mcp add lore-remote \
  --url https://lore.example/mcp \
  --bearer-token-env-var LORE_MCP_TOKEN
```

Equivalent current configuration:

```toml
[mcp_servers.lore-remote]
url = "https://lore.example/mcp"
bearer_token_env_var = "LORE_MCP_TOKEN"
default_tools_approval_mode = "writes"
tool_timeout_sec = 60
```

Use a token mapped to a `query`-only principal unless remote mutation is
actually required.

### Claude Code

The current CLI accepts a static authorization header:

```bash
claude mcp add --transport http --scope local lore-remote \
  https://lore.example/mcp \
  --header "Authorization: Bearer replace-with-token"
```

That form stores the header in Claude Code's private local configuration. For a
shareable `.mcp.json`, use environment expansion instead:

```json
{
  "mcpServers": {
    "lore-remote": {
      "type": "http",
      "url": "https://lore.example/mcp",
      "headers": {
        "Authorization": "Bearer ${LORE_MCP_TOKEN}"
      },
      "timeout": 60000
    }
  }
}
```

Set `LORE_MCP_TOKEN` before starting Claude Code. Do not commit the token or a
literal authorization header.

## Permissions and sensitivities

Tool discovery is filtered by permission, and every invocation checks again:

| Permission | Capabilities |
|---|---|
| `query` | `lore_search`, `lore_read`, page resources, page/source resource templates |
| `capture` | `lore_capture` |
| `curate` | `lore_preview`, `lore_commit`, transaction list/show/discard |
| `inspect` | `lore_lint`, `lore_index_status` |
| `history` | `lore_recent` |

Permissions are additive. `curate` does not imply `query`, and `inspect` does
not imply history. Give a principal only the permissions its client needs.

Each principal also has an allowlist of `normal`, `sensitive`, and/or
`local-only`. Search filters before returning results; direct unauthorized
reads look the same as nonexistent documents. HTTP principals cannot be
configured for `local-only`, and the core rejects that combination
defensively. History and inspection results suppress metadata that would
reveal inaccessible knowledge.

Transactions belong to the principal that previewed them. Another principal
cannot enumerate, show, discard, or commit them. Commit reauthorizes every
affected document against current sensitivity metadata.

## Tools and resources

All successful structured tool results use schema version `1`. Search and read
outputs include provenance, exact line information, and SHA-256 revisions.
Read responses are bounded; request another line range when `more` is true.

Capture and commit accept optional idempotency keys. Reuse a key only for the
same principal, operation, and exact input. A mismatched reuse fails rather
than repeating a different write.

Resource URIs are canonical ID-only forms:

```text
lore://pages/page_example
lore://sources/src_01ARZ3NDEKTSV4RRFFQ69G5FAV
```

Resource responses are private, zero-TTL, bounded, and policy-filtered.
Search results contain a matching `resource_uri`; it can be used with MCP
`resources/read` or passed back to `lore_read`. Resources are convenient reads,
not a way around tool authorization.

## Hosted clients

Hosted clients need network reachability to Lore and an authentication flow the
host supports. ChatGPT connector availability, workspace policy, networking,
and authentication can change independently of Lore and are not part of the
v0.4 release promise. A private tunnel, OAuth-capable proxy, or future
connector can be added outside Lore without changing the knowledge format.

## Troubleshooting

- Run `lore mcp check-config --config /etc/lore/mcp.yaml` before starting HTTP.
- Use `codex mcp get lore`, `claude mcp list`, or Claude Code's `/mcp` panel to
  inspect connection state. Avoid sharing `claude mcp get` output for a server
  with static headers because current clients may display the header value.
- An absent tool normally means that principal lacks its permission.
- A not-found read may mean either nonexistent or unauthorized content; this
  ambiguity is intentional.
- HTTP `401` means the authorization header is missing, malformed, duplicated,
  or invalid. Lore returns the same public response for all of them.
- HTTP `403` commonly means a rejected `Origin`; configure the exact scheme,
  host, and effective port, with no path.
- HTTP `404` means the endpoint path is wrong. Health checks are only
  `/health/live` and `/health/ready`.
- HTTP `413`, `429`, `503`, or a timeout indicates a configured body,
  concurrency, response, or time bound. Narrow the operation; do not disable
  the safety control reflexively.
- If stdio fails immediately, run the exact configured Lore command in a
  terminal. Confirm the executable path, repository path, profile, and that
  stdout is not wrapped by a banner-producing script.
- An active recovery journal intentionally blocks writes while reads remain
  available. Follow [the recovery guide](recovery.md).
- Treat client/model transcripts as a separate disclosure boundary. Lore's
  redacted audit log does not control what a client retains.

The Codex examples were checked against the current installed CLI and
[current OpenAI Codex MCP documentation](https://learn.chatgpt.com/docs/extend/mcp)
on 2026-07-29. The Claude Code examples were checked against the installed CLI
and
[current Claude Code MCP documentation](https://code.claude.com/docs/en/mcp)
on the same date.
