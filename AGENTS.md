# Lore development instructions

Lore is a deterministic Go CLI for maintaining a Markdown-and-Git personal knowledge repository.

## Required checks

Before finishing a code change, run:

- `gofmt` on changed Go files
- `go vet ./...`
- `go test ./...`
- `go test -race ./...` for concurrency, lock, filesystem, or Git changes
- `go build ./cmd/lore`

## Engineering rules

- Keep the Markdown repository authoritative.
- Do not add an LLM call, database, daemon, MCP server, or HTTP API in v0.2.
- Keep the CLI adapter thin; core operations return typed values and errors.
- Never invoke a shell; pass argument arrays to external commands.
- Centralize repository path validation and reject traversal or symlink escape.
- Never log or include captured source bodies in errors.
- Preserve exact source body bytes and verify them with SHA-256.
- Keep JSON output deterministic and backward-compatible within schema version 1.
- Add focused tests for every bug fix and behavior change.
- Avoid new dependencies unless the standard library is materially inadequate.
- Update `STATUS.md` when a milestone, material decision, or known limitation changes.
