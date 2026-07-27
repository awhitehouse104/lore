# Lore

Lore is a private, deterministic personal-knowledge layer backed by Markdown and Git. The Markdown repository is authoritative; Lore captures exact source material and provides read, search, lint, and history operations for humans and agent clients.

Lore does not call an LLM, answer natural-language questions, maintain a database, or run as a daemon.

## Development

Lore requires Go 1.26 and Git.

```bash
make check
make test-race
make build
```

User-facing installation, quickstart, command examples, repository behavior, and limitations will be completed with the v0.1 implementation.
