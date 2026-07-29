# Lore configuration

`lore.yaml` is strict YAML with schema version `1`. Unknown keys, multiple YAML
documents, invalid types, and out-of-range values fail before an operation
starts.

The complete v0.3 configuration is:

```yaml
version: 1

git:
  auto_commit_captures: true
  auto_push_captures: false
  auto_push_transactions: false
  remote: origin
  require_push: false

capture:
  max_bytes: 4194304

index:
  backend: auto
  auto_refresh_existing: true
  candidate_multiplier: 20
  minimum_candidates: 200
  maximum_candidates: 2000
```

Omitted fields receive these defaults.

## Git

- `auto_commit_captures` creates one path-limited commit after a successful
  capture.
- `auto_push_captures` pushes a capture commit unless the command overrides it.
- `auto_push_transactions` pushes a transaction commit unless overridden.
- `remote` is a Git remote name. It cannot begin with `-` or contain whitespace
  or control characters.
- `require_push` makes a requested/configured push failure return exit code 3.
  The local canonical commit remains safe.

## Capture

`capture.max_bytes` is the maximum exact input size. It must be positive and no
greater than 67,108,864 bytes (64 MiB). The default is 4,194,304 bytes (4 MiB).

## Index

- `backend` is `auto`, `index`, or `filesystem` and supplies the default when
  search has no `--backend`.
- `auto_refresh_existing` enables best-effort update after successful capture
  and transaction writes. It never creates an index.
- `candidate_multiplier` multiplies the requested public result limit to form
  an internal FTS candidate bound.
- `minimum_candidates` and `maximum_candidates` clamp that bound.

Bounds are:

| Field | Minimum | Maximum |
|---|---:|---:|
| `candidate_multiplier` | 1 | 100 |
| `minimum_candidates` | 1 | 10,000 |
| `maximum_candidates` | 1 | 100,000 |

`maximum_candidates` must be at least `minimum_candidates`. Candidate settings
affect performance and whether a `candidate_limit_reached` warning appears;
they do not change Lore's public scorer.

## Compatibility

v0.3 retains repository config schema version `1`. Existing v0.2
configurations without an `index` block are valid and receive v0.3 defaults.
Because v0.2 parsing is strict, v0.2 binaries reject the new `index` block.
During mixed-version operation, omit the block; v0.3 `auto` still falls back to
the filesystem when no index exists.

Similarly, v0.1 rejects `git.auto_push_transactions`. Omit fields unknown to
the oldest active client until every client is upgraded.
