# Lore MCP deployment

Lore v0.4's HTTP listener is intentionally plaintext. Keep it on loopback and
put an encrypted, access-controlled edge in front of it. Tailscale Serve is the
recommended remote posture for a personal deployment. Caddy with a custom
domain is a separate alternative when clients cannot join the tailnet. Public
unauthenticated binding is unsupported.

## Files and identities

Prefer a dedicated unprivileged `lore` account. A representative layout is:

```text
/usr/local/bin/lore             root:root 0755
/etc/lore/                      root:lore 0750
/etc/lore/mcp.yaml              root:lore 0640
/etc/lore/tokens/remote-reader  lore:lore 0600
/srv/lore/home/                 lore:lore 0700
/var/lib/lore/                  lore:lore 0700
```

Token files must be clean absolute paths, regular files, not symlinks, and must
not grant group or other permissions. They contain one hexadecimal or
base64url bearer token encoding 32–1,024 bytes, with an optional final newline.
Generate a 32-byte hexadecimal token without placing it in shell history:

```bash
umask 077
openssl rand -hex -out /etc/lore/tokens/remote-reader 32
```

Review ownership and mode after moving or restoring token files.

## Choose one edge

Do not stack the examples initially. They have different reachability and
certificate boundaries:

| Edge | Who can reach TCP 443 | TLS identity | Application authorization |
|---|---|---|---|
| Tailscale Serve (recommended) | Tailnet sources allowed by the tailnet policy | The node's MagicDNS name | Lore bearer token |
| Caddy custom domain | Sources allowed by DNS, host firewall, and upstream network controls | The configured public/private DNS name | Lore bearer token |

In both cases Lore listens only on `127.0.0.1:8787`, independently validates
the bearer token, and ignores proxy identity and forwarded-address headers.
Neither edge should expose `/health/live` or `/health/ready`; same-host
monitoring calls those paths directly over loopback.

The health handlers are intentionally unauthenticated and do not consume a
principal rate-limit token. Readiness also performs a fixed set of local
filesystem checks. If an edge configuration departs from the supplied exact
`/mcp` examples, add explicit health-path denials and verify them remotely;
never publish a broad prefix that reaches Lore's health routes.

Lore gives each configured bearer principal a generous bounded token bucket:
128 requests may arrive immediately, followed by 600 per minute. This is a
safety fuse for a tight authenticated loop, not an expected agent quota.
Invalid bearer values do not create or consume principal buckets. For a public
Caddy deployment, apply any unauthenticated source-rate control at the provider,
firewall, or another trusted edge without recording authorization headers or
MCP bodies. Tailscale deployments should first restrict reachability with the
shipped grants.

## Topology 1: Tailscale Serve (recommended)

This topology is:

```text
tailnet client
  -> HTTPS https://lore-vps.<tailnet>.ts.net/mcp
  -> Tailscale Serve on TCP 443
  -> HTTP http://127.0.0.1:8787/mcp
  -> Lore bearer authorization
```

Install Tailscale from its
[official Linux instructions](https://tailscale.com/docs/install/linux), join
the server to the intended tailnet, enable MagicDNS and HTTPS certificates,
and assign the node the `tag:lore-server` tag. Do not place a reusable
Tailscale auth key in this repository, a unit file, or shell history.

Merge [the example grants](examples/tailscale-grants.hujson) into the tailnet
policy after replacing `you@example.com`. The example allows only
`group:lore-clients` to reach TCP 443 on tagged Lore servers and tests that the
same identity cannot reach Lore's backend port 8787. Tailscale grants are
additive: remove or narrow any existing default/broad grant or ACL that already
allows those sources to reach the server, because a more specific grant cannot
revoke broader access. Save the policy only after its tests pass.

Start with [the Tailscale Serve Lore
configuration](examples/mcp-tailscale-serve.yaml). It deliberately grants a
`query`-only, `normal`-only principal. Add mutation permissions or `sensitive`
access only for a concrete client that needs them. Create the matching token,
check the configuration, and install [the systemd service](#systemd) before
starting Lore:

```bash
sudo install -d -o root -g lore -m 0750 /etc/lore
sudo install -d -o lore -g lore -m 0700 /etc/lore/tokens
sudo cp docs/examples/mcp-tailscale-serve.yaml /etc/lore/mcp.yaml
sudo chown root:lore /etc/lore/mcp.yaml
sudo chmod 0640 /etc/lore/mcp.yaml
sudo -u lore openssl rand -hex -out /etc/lore/tokens/tailnet-reader 32
sudo chmod 0600 /etc/lore/tokens/tailnet-reader
sudo -u lore lore mcp check-config --config /etc/lore/mcp.yaml
sudo systemctl restart lore-mcp.service
curl --fail http://127.0.0.1:8787/health/ready
```

Then publish only the MCP mount through Tailscale Serve:

```bash
sudo tailscale serve --bg --https=443 --set-path=/mcp \
  http://127.0.0.1:8787/mcp
sudo tailscale serve status
```

The `/mcp` target suffix is intentional: Serve removes its external mount
prefix before proxying, so the target restores Lore's exact backend path.
Background Serve configuration survives terminal exit and daemon restarts.
Record the exact `https://...ts.net/mcp` URL shown by `tailscale serve status`;
clients must be on the tailnet, allowed by policy, and configured with the
separate Lore bearer token.

Tailscale Serve adds identity headers, but Lore does not treat them as
authorization. Do not enable Funnel for this HTTPS port: Funnel is the
public-internet product and changes the trust boundary. Re-run `tailscale serve
status` after Tailscale upgrades or configuration changes and confirm it says
the service is available within the tailnet, not on the internet.

### Direct tailnet binding is an advanced fallback

Lore can instead bind plaintext to the node's exact stable Tailscale IP with
`allow_plaintext_non_loopback: true`. This is less contained: every process and
network path allowed to that IP and port reaches Lore directly, startup may
race the Tailscale interface, and HTTPS certificate handling moves to the
client/application boundary. If unavoidable, never use a hostname or
`0.0.0.0`, allow only the Lore port in tailnet policy and the host firewall,
order startup after Tailscale is online, and keep bearer authorization. There
is intentionally no direct-bind quick-start example.

## Topology 2: Caddy with a custom domain

Use this topology only when a required client cannot join the tailnet. Start
with [the loopback Lore configuration](examples/mcp-loopback.yaml) and
[the Caddyfile](examples/Caddyfile). Replace `lore.example.com` with a dedicated
DNS name whose A/AAAA records point only to the intended server.

The example requires Caddy 2.10 or newer. It:

- routes the exact `/mcp` path and returns `404` for everything else, including
  Lore's health paths;
- preserves `Authorization` using Caddy's default reverse-proxy behavior;
- limits request bodies to Lore's default 8 MiB and bounds client/upstream
  headers, reads, writes, idle time, dialing, and response-header waiting;
- adds private/no-store response headers; and
- leaves HTTP access logging disabled and never enables credential logging or
  body logging. Caddy's separate runtime/error logger removes its complete
  request object so upstream failures cannot retain headers or request targets.

If Lore's `request_max_bytes` or `request_timeout` changes, update the proxy
body limit and timeouts at the same time. The proxy limit should be no larger
than Lore's limit; its upstream response deadline should be slightly longer
than Lore's request deadline.

Install Caddy from its [official package
instructions](https://caddyserver.com/docs/install), then format and validate
the edited configuration before loading it:

```bash
sudo cp docs/examples/Caddyfile /etc/caddy/Caddyfile
sudo caddy fmt --overwrite /etc/caddy/Caddyfile
sudo caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy
sudo systemctl status caddy
```

Caddy's automatic public certificates ordinarily require working public DNS
and inbound TCP 80 and/or 443 reachability for ACME validation. Configure the
host firewall and provider firewall deliberately; do not open Lore's port 8787.
After issuance, confirm the certificate name and expiry from a separate client.
If the endpoint need not be internet-wide, restrict TCP 443 at the firewall or
another authenticated edge to known client networks. A public DNS name and a
valid certificate provide encryption and server identity, not client
authorization; the Lore bearer token remains mandatory.

Lore does not trust forwarded headers in v0.4. Leave
`transport.trust_forwarded_headers: false`. Configure browser origins, if any,
as exact normalized origins in `allowed_origins`. Empty means requests carrying
an `Origin` header are rejected while non-browser clients without one remain
allowed.

### Optional authentication-denial observability

Lore records every HTTP authentication denial as a warning with one fixed
reason:

- `missing_credentials` means no `Authorization` field reached Lore; and
- `invalid_credentials` means one or more fields were present but the bearer
  credential was malformed, duplicated, or did not match a configured token.

Both cases receive the same external `401`, and the audit event contains no
address, attempted identity, token, token hash, header, request target, query,
or body. Under both recommended topologies Lore's direct peer is the loopback
proxy. Do not build a source-address ban from the Lore event: it would identify
and potentially ban Caddy or Tailscale Serve at `127.0.0.1`, not the remote
client. Lore never reads `X-Forwarded-For` or Tailscale identity headers.

The standard Caddy example therefore keeps access logging off. When a
public-Caddy deployment has a reviewed need for source attribution, use the
separate opt-in
[authentication-observability Caddyfile](examples/Caddyfile-auth-observability).
It records only exact `/mcp` requests, deletes every request header and all
request fields except the direct source IP, replaces the URI with constant
`/mcp`, and retains only Caddy's built-in logger fields, timestamp, and status.
The log covers successful as well as denied MCP requests because the response
status is known only after routing. Its separate runtime/error stream removes
the complete request object. The access record is personal metadata even
though neither stream contains a credential, variable request target, or MCP
content.

Before loading the opt-in example, create its private output directory and
review the seven-day/10-MiB rolling limits:

```bash
sudo install -d -o caddy -g caddy -m 0750 /var/log/caddy
sudo cp docs/examples/Caddyfile-auth-observability /etc/caddy/Caddyfile
sudo caddy fmt --overwrite /etc/caddy/Caddyfile
sudo caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

The accompanying Fail2ban examples consume that Caddy file, never Lore's
journal:

```bash
sudo install -D -m 0644 \
  docs/examples/fail2ban/filter.d/lore-caddy.conf \
  /etc/fail2ban/filter.d/lore-caddy.conf
sudo install -D -m 0644 \
  docs/examples/fail2ban/jail.d/lore-caddy.local \
  /etc/fail2ban/jail.d/lore-caddy.local

sudo fail2ban-regex \
  /var/log/caddy/lore-auth.json \
  /etc/fail2ban/filter.d/lore-caddy.conf
sudoedit /etc/fail2ban/jail.d/lore-caddy.local
# Set enabled = true only after reviewing thresholds and recovery access.
sudo fail2ban-client -t
sudo systemctl restart fail2ban
sudo fail2ban-client status lore-caddy
```

The example waits for 30 failures from one address within five minutes, then
bans that address from HTTPS for 15 minutes. These are deliberately forgiving
runaway-source defaults, not a claim that bearer-token guessing is practical.
Tune only after observing legitimate clients, and merge any existing trusted
addresses into the jail's explicit loopback `ignoreip` value. Keep independent
SSH or console recovery access; an accidental ban can be removed with
`sudo fail2ban-client set lore-caddy unbanip ADDRESS`.

The filter is intentionally anchored to the complete minimized Caddy record so
untrusted text cannot become a ban target. Re-run `caddy validate`,
`fail2ban-regex`, and `fail2ban-client -t` after every Caddy or Fail2ban upgrade;
if the structured field order changes, update and retest the exact expression
instead of loosening it with broad catch-alls.

Do not use this jail if another CDN or proxy connects to Caddy: `remote_ip`
would identify that intermediary until Caddy's trusted-proxy boundary is
separately configured and tested. Tailscale deployments should use narrow
grants as the primary source control. Optional Tailscale network-flow logging
is plan-dependent connection metadata and does not identify which HTTP request
received `401`; it is not a substitute input for this jail.

## Deployment smoke test

Run these checks from an authorized remote client, substituting the URL shown
by Tailscale Serve or the Caddy domain. Load the token into
`LORE_MCP_TOKEN` without putting its value in shell history, and do not use
`curl -v`:

```bash
export LORE_MCP_URL=https://lore-vps.example.ts.net/mcp

curl --fail-with-body --silent --show-error \
  --header "Authorization: Bearer ${LORE_MCP_TOKEN}" \
  --header "Content-Type: application/json" \
  --header "Accept: application/json, text/event-stream" \
  --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"deployment-smoke","version":"1"}}}' \
  "${LORE_MCP_URL}"

curl --silent --output /dev/null --write-out '%{http_code}\n' \
  --data '{}' "${LORE_MCP_URL}"

curl --silent --output /dev/null --write-out '%{http_code}\n' \
  "${LORE_MCP_URL%/mcp}/health/ready"
```

The initialize request must succeed, the request without a token must return
`401`, and the edge health-path request must return `404`. On the server,
loopback liveness and readiness must still return `200`. Review Lore and edge
logs for the smoke-test interval. The default edge examples retain no access
log; the opt-in Caddy example retains the minimized source/status record
described above. No supported log should contain the bearer value or MCP body.

If an authenticated client receives `429`, wait for the integer seconds in
`Retry-After` before retrying. The same response also protects the global
concurrency boundary. Occasional `429` during an intentionally extreme burst
does not indicate repository damage; repeated responses warrant correcting the
client loop or deliberately increasing the external configuration limit.

Finally reboot the server. Confirm Lore readiness, the selected edge's status,
the remote initialize request, unauthorized `401`, and edge health-path `404`
again. Also verify the host and provider firewalls expose only the ports the
chosen topology requires.

## systemd

Install and adjust [the example unit](examples/lore-mcp.service), then:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now lore-mcp.service
sudo systemctl status lore-mcp.service
```

Validate the hardening set on the target distribution:

```bash
sudo systemd-analyze security lore-mcp.service
```

The sample unit sets `MemoryHigh=384M` as a reclaim-pressure threshold and
`MemoryMax=512M` as a hard cgroup limit. Those values are a starting envelope
for the default 8 MiB payload limits, eight-request concurrency, and a modest
repository—not a universal sizing claim. Lore caps configured in-flight
request payloads at 64 MiB and response payloads at 64 MiB, but parsing,
serialization, whole-response buffering, repository/index working sets, Git,
and the Go runtime add overhead. Monitor `MemoryCurrent` and peak memory on the
target workload; increase both limits together when justified, retaining
meaningful headroom between `MemoryHigh` and `MemoryMax`. A `MemoryMax` breach
can terminate the service, after which `Restart=on-failure` restarts it.
Authenticated MCP requests retain their concurrency slots through bounded
response publication, including time spent writing to slow clients. This makes
the configured response-payload calculation meaningful. The two
whole-response buffers can temporarily hold roughly twice that payload
capacity, and repository scans and the other working sets listed above remain
outside it.

`ProtectHome=true` means repositories and SSH material beneath `/home` are not
available. Use explicit service-owned paths such as `/srv/lore/home` and
`/var/lib/lore`. If Git push is enabled, the service needs DNS/TLS access or a
narrowly scoped SSH deploy key, known-host verification, and access to the
required credential files. Do not attach a user's general SSH agent.

The sample sets `HOME=/var/lib/lore` so Git and SSH have a private service home.
Add only the minimum `ReadWritePaths` and credential reads needed on the actual
host. Test startup, a request, SIGTERM shutdown, restart, Git commit, and any
configured push after changing sandbox directives.

Git is deliberately noninteractive under Lore. For SSH, provision a
service-owned, unlocked deploy key with the narrowest remote permission and pin
the host key, or expose a dedicated pre-unlocked agent socket through
`SSH_AUTH_SOCK`. For HTTPS, configure a trusted credential helper for the
service account and verify it succeeds without a terminal. Askpass and terminal
prompts are disabled, so encrypted keys or helpers that require interactive
unlocking fail instead of hanging.

Local Git operations time out after 30 seconds and pushes after two minutes;
HTTP request and service-shutdown deadlines may be shorter. A timeout kills the
whole Git process group. Repository hooks, filesystem monitors, signing,
automatic maintenance, and active content filters on managed paths are also
disabled or rejected. Treat any configured credential helper or SSH command as
trusted executable service configuration.

Use liveness to decide whether the process needs restarting. It performs no
repository I/O and returns `200 {"status":"ok"}` whenever the HTTP handler can
answer. Use readiness for routing and operational alerts. It returns the same
`200` only while:

- the repository root is still the directory opened at startup;
- the startup `lore.yaml` remains the same accessible regular non-symlink file;
- `pages/`, `sources/`, `assets/`, `system/`, and `.lore/` remain accessible
  real directories; and
- no `.lore/recovery/active` entry exists.

Any readiness failure returns only
`503 {"status":"unavailable"}`. An active recovery therefore makes readiness
fail even though Lore deliberately keeps authorized reads available. This
principal-independent fail-closed contract makes standard HTTP probes useful
without exposing the recovery phase, repository path, principal, Git state, or
other diagnostic metadata. Run `lore recover` or `lore lint` locally for the
details.

Readiness performs a fixed number of path metadata/open checks. It never walks
or parses Markdown, reparses YAML, runs lint or Git, inspects the index, contacts
a remote, or acquires the repository lock. Ordinary write-lock contention and
individual malformed documents do not flap the probe. Changing `lore.yaml`
requires a service restart so the running process and readiness baseline load
the same configuration.

`Restart=on-failure` does not react to a readiness-only `503` while the Lore
process continues serving liveness. Alert on sustained readiness failure and
diagnose it locally. For a planned configuration change, run `lore mcp
check-config`, restart `lore-mcp.service`, and require readiness to return `200`
before restoring traffic. Do not build a blind readiness-triggered restart loop:
an invalid configuration or damaged repository needs operator-visible repair.

```bash
curl --fail http://127.0.0.1:8787/health/live
curl --fail http://127.0.0.1:8787/health/ready
```

## Token rotation

Lore loads token files only when the process starts. A safe overlap rotation is:

1. Generate a new token in a new mode-`0600` file.
2. Add a temporary second principal entry with the same least-privilege grants
   and a distinct principal name.
3. Run `lore mcp check-config`.
4. Restart the service and verify the new client credential.
5. Remove the old principal, restart, and confirm the old credential receives
   `401`.
6. Securely retire the old file according to the host's storage and backup
   policy.

Principal IDs own transaction and idempotency state. Rotating only the token
file while retaining the same principal name preserves that ownership. Using a
temporary second name does not transfer open transactions; finish or discard
them before removing the old principal.

An atomic in-place token-file replacement followed by restart is also valid but
has no overlap window. Never reuse one token for two principals; strict config
validation rejects duplicate token digests.

## Backups and incident response

Back up canonical Markdown and complete Git history. Separately decide whether
to back up `.lore/` derived state; it can contain private page bytes, diffs,
idempotency receipts, and recovery originals. Token files and service
credentials need a distinct secret-backup policy.

For a suspected token disclosure:

1. replace or remove the credential and restart Lore;
2. inspect metadata-only service audit events and any explicitly enabled,
   privacy-reviewed reverse-proxy access logs;
3. review Git and repository state with `lore lint` and `lore recent --all`;
4. resolve any active recovery journal explicitly;
5. rotate downstream Git credentials if exposure may include them.

Lore audit events intentionally omit tokens, queries, snippets, bodies, diffs,
and unauthorized titles/paths. Preserve that property in proxy, service
manager, and client logging.

## Transaction retention

Preview artifacts can retain complete private page bytes and grow by tens of
MiB per transaction. Review retention manually before automating it:

```bash
sudo -u lore lore --repo /srv/lore/home \
  transaction prune --older-than 30d --limit 10 --dry-run
sudo -u lore lore --repo /srv/lore/home \
  transaction prune --older-than 30d --limit 10 --json
```

The command is bounded, oldest-first, write-locked, and resumable. A concurrent
writer or active recovery journal makes it fail closed; retry after resolving
that condition. It holds the write lock while Git reachability, changed paths,
and committed blobs are checked during full preflight and again before each
compaction. Captures and commits that exhaust their bounded lock wait therefore
fail during a long pruning run. Schedule it while agents are idle, begin with a
small `--limit`, and increase the batch only after measuring the target
repository. Lore intentionally ships no retention setting or systemd timer in
this first version. After the manual policy and restore implications have been
reviewed on the actual repository, an operator-controlled timer may invoke the
same explicit command. Pruning is not a substitute for Git-history, backup,
snapshot, or media-erasure policy.
