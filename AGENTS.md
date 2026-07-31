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
- Treat the completed v0.4 architecture as the baseline: Lore ships a CLI, a
  disposable SQLite search index, local stdio MCP, and a permissioned stateless
  HTTP MCP gateway.
- Do not add a server-side LLM call, embeddings or vector retrieval, an
  authoritative database, autonomous background processing, or a new
  transport/API without explicitly approved release scope.
- Keep SQLite repository-bound, disposable, and derived from authoritative
  Markdown.
- Keep CLI and MCP adapters thin; core operations return typed values and
  errors.
- Keep remote-server configuration and credentials outside the knowledge
  repository, and preserve the structural exclusion of `local-only` content
  from HTTP.
- Never invoke a shell; pass argument arrays to external commands.
- Centralize repository path validation and reject traversal or symlink escape.
- Never log or include captured source bodies in errors.
- Preserve exact source body bytes and verify them with SHA-256.
- Keep JSON output deterministic and backward-compatible within schema version 1.
- Add focused tests for every bug fix and behavior change.
- Avoid new dependencies unless the standard library is materially inadequate.
- Update `STATUS.md` when a milestone, material decision, or known limitation changes.
