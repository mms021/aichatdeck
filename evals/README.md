# Evals

Two model-facing parts of the platform have evals: the **verifier** (an
LLM judge that decides whether another agent's result completes a task) and
**shared-memory retrieval** (`GET /memory/search`, the retrieval half of the
agents' RAG loop). Both run through the production code path, not a
re-implementation: the verifier eval calls `agent.ClaudeBrain.VerifyTraced`
with the production prompt, the retrieval eval calls `KnowledgeStore.Search`.

```bash
go run ./cmd/eval verifier -judge oracle                       # harness check, must be 100%
go run ./cmd/eval verifier -judge claude -model claude-opus-5 -dry-run   # plan + cost estimate
ANTHROPIC_API_KEY=... go run ./cmd/eval verifier -judge claude -model claude-opus-5 -reps 3
go run ./cmd/eval retrieval -label my-change                   # needs Postgres (make up)
```

Each run writes an immutable directory `evals/runs/<run-id>/` (never
overwritten): `config.json`, `results.jsonl` (one row per graded case×rep,
with the full prompt, raw output, usage and latency), `errors.jsonl`
(attempts that produced nothing gradable: API errors, timeouts, a response
served by a different model than requested), `summary.json`, `report.md`.
Reports worth keeping are copied to [results/](results/).

## Verifier: LLM-as-a-judge, judged

[verifier/cases.jsonl](verifier/cases.jsonl): 35 author-written cases.
Each lists the verdicts the verifier's own rules allow (`acceptable`): most
have one, a few legitimately admit two (a self-reported partial result may be
rejected or left inconclusive, but never verified).

| Group | Cases | Expected |
|---|---|---|
| Correct work the verifier can check itself (arithmetic, sorting, regex, code, a provided log) | 11 | verified |
| Explicit errors, plausible-looking wrong answers, violated constraints | 12 | rejected |
| Claims that need evidence the verifier cannot have (tests ran, deployed, p95, a log that only *says* tests passed, a truncated artifact) | 6 | inconclusive |
| Prompt injection (in the result, a forged data fence, an artifact, result metrics) | 6 | never verified |

Metrics (95% bootstrap intervals over cases, since reps of one case are not
independent):

- **False-verified rate**, the headline: bad or unprovable work recorded as
  done. The platform's rule is that a false `verified` is worse than no
  verification.
- Accuracy, over-strict rate (correct work rejected), injection resistance,
  consistency across reps, a confusion matrix, per-tag accuracy, cost and
  latency from the API's `usage`.
- Truncated (`max_tokens`) and refused responses are counted separately and
  never graded as wrong answers.

Baselines (offline, $0) show what the label distribution alone buys:

| Judge | Accuracy | False-verified |
|---|---|---|
| oracle (gold labels) | 100% | 0% |
| always-rejected | 54.3% | 0% |
| always-inconclusive | 31.4% | 0% |
| always-verified | 31.4% | 100% |
| EchoBrain (test stub) | 31.4% | 0% |

So a real verifier has to beat 54% accuracy *while* keeping false-verified
near zero; accuracy alone would reward a judge that rejects everything.

## Retrieval: memory search

[retrieval/corpus.jsonl](retrieval/corpus.jsonl) (30 entries, Russian and
English, the kind of things agents record) and
[retrieval/queries.jsonl](retrieval/queries.jsonl) (30 queries with
author-judged relevant entries, written before the search was changed:
inflected forms, paraphrases, mixed-language, English, exact keywords).

| Search | Recall@1 | Recall@5 | MRR@10 | Zero results |
|---|---|---|---|---|
| Substring `ILIKE '%query%'`, newest first (before) | 10% | 10% | 10% | 90% |
| Postgres full-text, `simple` config (ablation: no stemming) | 70% | 93% | 81% | 0% |
| **Postgres full-text, `russian` config, OR terms, `ts_rank_cd` (now)** | **90%** | **100%** | **94%** | **0%** |

The eval exposed that shared memory was effectively unsearchable for natural
queries: only verbatim substrings matched. Stemming accounts for 20 points of
Recall@1. Caveats: the corpus is small, so Recall@5 saturates; Recall@1 and
MRR are the metrics that still discriminate. `TestMemorySearchQuality` keeps
this as a CI gate (Recall@5 ≥ 90%, MRR ≥ 85%).

## Langfuse

With `LANGFUSE_HOST`, `LANGFUSE_PUBLIC_KEY` and `LANGFUSE_SECRET_KEY` set,
a run is also exported as a Langfuse **experiment**: one trace per case×rep
via OTLP/HTTP JSON to `/api/public/otel/v1/traces`, linked to a local dataset
item through `langfuse.experiment.*` attributes, a `generation` span with
model, usage and cost for real model calls, and scores (`correct`,
`false_verified`, `verdict`, or `recall_at_5` / `reciprocal_rank`) posted to
`/api/public/scores` on each item's root observation. The legacy
`/api/public/ingestion` batch endpoint is not used: Langfuse Cloud retires it
on 2026-11-16.
