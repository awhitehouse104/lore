# Lore v0.3 dependency and security audit

Audit date: 2026-07-29.

Lore builds with Go 1.26 and embeds a pure-Go SQLite/FTS5 implementation. The
installed binary does not require a system SQLite library, C toolchain,
database server, network service, or LLM SDK.

## Compiled runtime closure

`go list -deps ./cmd/lore` selected these non-standard modules:

| Module | Version | License | Purpose |
|---|---|---|---|
| `github.com/oklog/ulid/v2` | `v2.1.2` | Apache-2.0 | Source, transaction, and index build identifiers |
| `go.yaml.in/yaml/v4` | `v4.0.0-rc.6` | Apache-2.0 | Strict configuration and Markdown frontmatter |
| `modernc.org/sqlite` | `v1.54.0` | BSD-3-Clause | Pure-Go SQLite and FTS5 |
| `github.com/dustin/go-humanize` | `v1.0.1` | MIT | SQLite transitive runtime support |
| `github.com/google/uuid` | `v1.6.0` | BSD-3-Clause | SQLite transitive runtime support |
| `github.com/remyoudompheng/bigfft` | `v0.0.0-20230129092748-24d4a6f8daec` | BSD-3-Clause | SQLite numeric transitive support |
| `golang.org/x/sys` | `v0.46.0` | BSD-3-Clause | Platform system calls |
| `modernc.org/libc` | `v1.74.1` | BSD-3-Clause plus documented third-party notices | Pure-Go libc layer |
| `modernc.org/mathutil` | `v1.7.1` | BSD-3-Clause | SQLite numeric transitive support |
| `modernc.org/memory` | `v1.11.0` | BSD-3-Clause plus bundled component notices | SQLite memory transitive support |

The standard library and Go toolchain use the Go BSD-style license.

## Selected module graph not compiled into `cmd/lore`

The complete selected graph also contains dependency build/test/tool modules:

- Apache-2.0: `github.com/google/pprof`;
- MPL-2.0: `github.com/hashicorp/golang-lru/v2`;
- MIT: `github.com/mattn/go-isatty`, `github.com/ncruces/go-strftime`;
- BSD-3-Clause: `github.com/pborman/getopt`, `golang.org/x/mod`,
  `golang.org/x/sync`, `golang.org/x/tools`, and the selected
  `modernc.org/cc/v4`, `ccgo/v4`, `fileutil`, `gc/v2`, `gc/v3`, `goabi0`,
  `opt`, `sortutil`, `strutil`, and `token` modules.

These classifications were checked against the license files in the exact
downloaded module versions. `go mod verify` reported `all modules verified`.

## Security checks

The release audit used:

```bash
go mod tidy -diff
go mod verify
govulncheck ./...
```

`govulncheck` v1.6.0 reported `No vulnerabilities found` for the v0.3 release
candidate on 2026-07-29. Vulnerability databases change; rerun the scanner for
future builds rather than treating this result as permanent.
