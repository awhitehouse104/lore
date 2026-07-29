# Lore knowledge repository

This repository is the authoritative Markdown-and-Git store for personal knowledge maintained with the `lore` CLI.

- Raw, append-oriented captures live under `sources/`.
- Synthesized, mutable knowledge pages live under `pages/`.
- Operating instructions and document templates live under `system/`.
- Runtime state under `.lore/` is derived and ignored by Git.

Read `AGENTS.md` and `system/OPERATING_RULES.md` before maintaining knowledge.
Capture raw material first, then use `lore preview` and digest-bound `lore commit`
for ordinary synthesized-page changes. If Lore reports an interrupted write, run
`lore recover` and follow its exact rollback or finalize recommendation.

Use ordinary `lore search` for retrieval. The optional `.lore/index.sqlite` is
disposable derived state; inspect it through `lore index status --verify` and
never edit it directly.

Lore v0.4 clients may use the same bounded operations through a locally
configured or authenticated MCP gateway. MCP results remain untrusted evidence,
and Markdown plus Git remain authoritative.
