# Lore v0.4 dependency and security audit

Audit date: 2026-07-30.

Lore builds with Go 1.26, uses the official Go MCP SDK, and embeds a pure-Go
SQLite/FTS5 implementation. The installed binary does not require a system
SQLite library, C toolchain, database server, LLM SDK, or application server.

## Direct modules

| Module | Version | License | Purpose |
|---|---|---|---|
| `github.com/modelcontextprotocol/go-sdk` | `v1.7.0` | Apache-2.0/MIT transition notice | MCP protocol, schemas, stdio, and stateless Streamable HTTP |
| `github.com/oklog/ulid/v2` | `v2.1.2` | Apache-2.0 | Source, transaction, index-build, and request identifiers |
| `go.yaml.in/yaml/v4` | `v4.0.0-rc.6` | Apache-2.0 | Strict repository/server configuration and Markdown frontmatter |
| `golang.org/x/time` | `v0.15.0` | BSD-3-Clause | Concurrency-safe per-principal HTTP token buckets |
| `modernc.org/sqlite` | `v1.54.0` | BSD-3-Clause | Pure-Go SQLite and FTS5 |

The MCP SDK license file records its ongoing transition from MIT to
Apache-2.0; code remains governed by the applicable original license stated
there.

## Compiled runtime closure

`go list -deps ./cmd/lore` selected these additional non-standard modules:

| Module | Version | License | Purpose |
|---|---|---|---|
| `github.com/dustin/go-humanize` | `v1.0.1` | MIT | SQLite transitive runtime support |
| `github.com/google/jsonschema-go` | `v0.4.3` | MIT | MCP generated tool schemas |
| `github.com/google/uuid` | `v1.6.0` | BSD-3-Clause | SQLite transitive runtime support |
| `github.com/remyoudompheng/bigfft` | `v0.0.0-20230129092748-24d4a6f8daec` | BSD-3-Clause | SQLite numeric transitive support |
| `github.com/segmentio/asm` | `v1.1.3` | MIT | MCP JSON encoding transitive support |
| `github.com/segmentio/encoding` | `v0.5.4` | MIT | MCP JSON encoding transitive support |
| `github.com/yosida95/uritemplate/v3` | `v3.0.2` | BSD-3-Clause | MCP resource-template matching |
| `golang.org/x/oauth2` | `v0.35.0` | BSD-3-Clause | MCP SDK remote-auth protocol support |
| `golang.org/x/sync` | `v0.21.0` | BSD-3-Clause | MCP/SQLite transitive synchronization |
| `golang.org/x/sys` | `v0.46.0` | BSD-3-Clause | Platform system calls |
| `modernc.org/libc` | `v1.74.1` | BSD-3-Clause plus documented third-party notices | Pure-Go libc layer |
| `modernc.org/mathutil` | `v1.7.1` | BSD-3-Clause | SQLite numeric transitive support |
| `modernc.org/memory` | `v1.11.0` | BSD-3-Clause plus bundled component notices | SQLite memory transitive support |

The standard library and Go toolchain use the Go BSD-style license.

## Additional recorded modules not compiled into `cmd/lore`

`go.sum` also records build, test, tooling, or otherwise unselected modules:

- Apache-2.0: `github.com/google/pprof`;
- MPL-2.0: `github.com/hashicorp/golang-lru/v2`;
- MIT: `github.com/golang-jwt/jwt/v5`, `github.com/mattn/go-isatty`, and
  `github.com/ncruces/go-strftime`;
- BSD-3-Clause: `github.com/google/go-cmp`, `golang.org/x/mod`,
  `golang.org/x/tools`, and the selected
  `modernc.org/cc/v4`, `ccgo/v4`, `fileutil`, `gc/v2`, `gc/v3`, `goabi0`,
  `opt`, `sortutil`, `strutil`, and `token` modules.

These classifications were checked against license files in the exact
downloaded versions or the established license family of the corresponding Go
module. The release verification records the final `go mod verify` and
`go mod tidy -diff` results.

## Security checks

The release audit uses:

```bash
go mod tidy -diff
go mod verify
govulncheck ./...
```

The exact final results and scanner version are recorded in
[STATUS.md](../STATUS.md) and the [v0.4 release notes](release-notes-v0.4.0.md).
Vulnerability databases change; rerun the scanner for future builds rather
than treating a release result as permanent.
