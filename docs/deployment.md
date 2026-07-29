# Lore MCP deployment

Lore v0.4's HTTP listener is intentionally plaintext. Deploy it on loopback
behind a TLS/authenticated reverse proxy, or on an independently encrypted and
access-controlled private tailnet. Public unauthenticated binding is
unsupported.

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

## Topology 1: loopback behind TLS

Start with [the loopback example](examples/mcp-loopback.yaml). Lore listens on
`127.0.0.1:8787`; a same-host reverse proxy terminates TLS and forwards only
the configured `/mcp` path.

The proxy must:

- preserve the `Authorization` header;
- enforce HTTPS and a suitable request-size limit;
- avoid logging authorization headers or MCP request/response bodies;
- restrict its public authentication and network policy independently;
- forward health endpoints only to internal monitoring when desired.

Lore does not trust forwarded headers in v0.4. Leave
`transport.trust_forwarded_headers: false`. Configure browser origins, if any,
as exact normalized origins in `allowed_origins`. Empty means requests carrying
an `Origin` header are rejected while non-browser clients without one remain
allowed.

## Topology 2: private tailnet

Start with [the tailnet example](examples/mcp-tailnet.yaml). Bind to the exact
tailnet IP, never `0.0.0.0` or a hostname. Set
`allow_plaintext_non_loopback: true` only when the surrounding network already
provides encryption, peer authentication, and an access policy limiting which
devices can reach the port.

The override means “the private transport supplies confidentiality”; it does
not make plaintext safe on an ordinary LAN or public interface. Bearer
authentication remains mandatory on every request.

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

`ProtectHome=true` means repositories and SSH material beneath `/home` are not
available. Use explicit service-owned paths such as `/srv/lore/home` and
`/var/lib/lore`. If Git push is enabled, the service needs DNS/TLS access or a
narrowly scoped SSH deploy key, known-host verification, and access to the
required credential files. Do not attach a user's general SSH agent.

The sample sets `HOME=/var/lib/lore` so Git and SSH have a private service home.
Add only the minimum `ReadWritePaths` and credential reads needed on the actual
host. Test startup, a request, SIGTERM shutdown, restart, Git commit, and any
configured push after changing sandbox directives.

Health endpoints return no repository or principal metadata:

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
2. inspect metadata-only service audit events and reverse-proxy access logs;
3. review Git and repository state with `lore lint` and `lore recent --all`;
4. resolve any active recovery journal explicitly;
5. rotate downstream Git credentials if exposure may include them.

Lore audit events intentionally omit tokens, queries, snippets, bodies, diffs,
and unauthorized titles/paths. Preserve that property in proxy, service
manager, and client logging.
