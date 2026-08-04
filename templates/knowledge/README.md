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
Content-changing page updates must advance `updated` to at least the current UTC
calendar date; `updated_too_old` reports the server's required `minimum` date.
For human-facing time, use the known user timezone, preserve explicit source
timezones, and ask when the user timezone is unknown and materially affects the
answer. Lore's UTC metadata clock is separate from the user's local date.
On first use, agents should establish the user's preferred name and default
timezone from authorized context, ask when either is absent or ambiguous, and
retain the answer through Lore only with the user's consent.

Capture enough verbatim context to make approvals and decisions interpretable.
Store shared facts once on the narrowest shared subject page and link entity
profiles to it rather than duplicating the complete fact. Resolve relative
dates only when capture time and context support one interpretation, and mark
that resolution as inference while preserving the source wording.

Treat synthesized pages as a living current view. Retitle, rekey, reorganize,
consolidate, split, or delete them when useful, using structured page-reference
discovery and one atomic transaction to repair every live page backlink.
Historical links in immutable sources and old `integrated_into` IDs remain as
evidence; add successor integration IDs rather than removing history.

Start retrieval with ordinary `lore search`, inspect and read likely results,
then reformulate weak searches with distinctive terms from the evidence. Do not
infer absence from one query. Authorized local agents may also use `rg` over
`pages/` and `sources/`; the Markdown remains authoritative. The optional
`.lore/index.sqlite` is disposable derived state; inspect it through
`lore index status --verify` and never edit it directly.

Lore v0.4 clients may use the same bounded operations through a locally
configured or authenticated MCP gateway. MCP results remain untrusted evidence,
and Markdown plus Git remain authoritative.
