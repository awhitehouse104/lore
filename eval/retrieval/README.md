# Retrieval evaluation

This directory is Lore's deterministic search-quality fixture. It measures
production behavior before and after search ranking, fuzzy matching, result
evidence, or agent-guidance changes.

The corpus is synthetic and contains no personal knowledge. It deliberately
uses no page aliases: retrieval improvements must work from titles, bodies,
canonical metadata, and query behavior rather than hand-authored alternate
names.

## Run it

From the Lore source root:

```bash
make eval-retrieval
```

For the complete machine-readable current report:

```bash
go run ./cmd/lore-retrieval-eval --json
```

The runner copies `corpus/` to a private temporary directory, builds a fresh
disposable SQLite index, and evaluates every case twice:

- `filesystem` forces the canonical Markdown scanner and ranker.
- `auto` exercises production backend selection against a stable, clean,
  Git-shaped evaluation snapshot. Queries unsuitable for indexed candidate
  generation retain the normal filesystem fallback.

The source corpus is never modified and the runner does not invoke Git.

## Measurements

The human report provides category and overall values for:

- hit@1, hit@3, hit@5, and hit@10;
- mean reciprocal rank (MRR);
- mean per-case recall@5;
- normalized discounted cumulative gain at 10 (nDCG@10);
- zero-result and returned-result counts;
- forbidden-result count for sensitivity-boundary cases;
- effective filesystem/index use; and
- exact result parity between forced filesystem and production `auto` search.

Hit@5 is the primary agent-retrieval measure: an agent can cheaply inspect a
small set of bounded snippets. MRR and nDCG preserve pressure toward useful
ordering, while result volume and forbidden-result counts keep broader recall
observable and safe.

`baseline.json` records the complete deterministic result lists, scores,
metrics, warnings, and backend choices. `go test ./...` reruns the suite and
fails when current behavior differs.

## Add a case

Cases in `suite.yaml` have this shape:

```yaml
- id: natural_rebuild_after_failure
  category: natural_question
  query:
    text: rebuild service after host failure
    scope: pages
    sensitivities: [normal]
  relevant:
    - id: page_disaster_recovery
      grade: 3
  forbidden_ids: []
```

Every case must:

- have a unique lowercase identifier and category;
- declare its sensitivity allowance explicitly;
- name at least one relevant canonical document;
- assign each relevant document a grade from 1 to 3; and
- keep relevant and forbidden IDs disjoint.

Optional `kind`, `tags`, `paths`, `limit`, and `matching` fields mirror
production search. Omitted `matching` is normalized to the production `auto`
default in the report.
Use multiple relevant documents when several results genuinely answer the
query; grades should express relative usefulness rather than make one arbitrary
page the only success.

Add or edit corpus documents as canonical Lore Markdown. Source fixture hashes
must match their exact body bytes. The index build validates the corpus before
running any query.

Prefer cases derived from observed agent failures, rewritten to remove private
details. Keep a balanced mix of direct vocabulary, natural questions,
morphology, typos, alternate vocabulary, distractors, sources, metadata, and
authorization boundaries. This synthetic suite is not a statistical estimate
of a real personal repository.

## Update the baseline

First run the ordinary command and inspect every reported metric, weak case,
authorization count, and parity change. When the new behavior is intentional:

```bash
go run ./cmd/lore-retrieval-eval --write-baseline
go test ./eval/retrieval
```

Do not refresh the baseline merely to make a test pass. A search change should
either improve a measured objective or document a deliberate tradeoff.

The checked-in baseline includes the reviewed lexical-ranking and adaptive
fuzzy-matching behavior: exact tag-token scoring, authorized-corpus rarity,
distinct-query-term coverage, indexed `kind` candidates, correction evidence,
and exact filesystem/index parity. Subsequent changes should be compared
against it rather than the historical phase metrics recorded in `STATUS.md`.
