# Claude Code instructions

Before modifying knowledge, read `AGENTS.md` and `system/OPERATING_RULES.md` and follow both.

Use `lore preview` and digest-bound `lore commit` for normal page maintenance.
Inspect the complete diff and lint result, and re-preview rather than forcing a
reported conflict.

Use normal `lore search` for retrieval. Diagnose an existing derived index with
`lore index status --verify` or `lore index update`; never edit its SQLite
database directly.
