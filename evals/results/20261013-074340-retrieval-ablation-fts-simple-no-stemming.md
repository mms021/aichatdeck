# Memory retrieval eval — ablation-fts-simple-no-stemming

Run `20261013-074340-retrieval-ablation-fts-simple-no-stemming` · 30 queries over 30 entries · GET /memory/search path (KnowledgeStore.Search), top 10

| Metric | Value (95% CI, bootstrap over queries) |
|---|---|
| Recall@1 | 70.0% [53.3–86.7] (n=30) |
| Recall@3 | 93.3% [83.3–100.0] (n=30) |
| **Recall@5** | 93.3% [83.3–100.0] (n=30) |
| MRR@10 | 80.6% [68.9–91.1] (n=30) |
| Zero-result rate (lower is better) | 0.0% [0.0–0.0] (n=30) |
| Latency p95 | 0.9 ms |

## Recall@5 by query type

| Type | Recall@5 |
|---|---|
| en | 100.0% [100.0–100.0] (n=4) |
| exact | 100.0% [100.0–100.0] (n=2) |
| inflection | 92.3% [76.9–100.0] (n=13) |
| keyword | 100.0% [100.0–100.0] (n=3) |
| mixed | 75.0% [25.0–100.0] (n=4) |
| paraphrase | 100.0% [100.0–100.0] (n=8) |
| ru | 91.3% [78.3–100.0] (n=23) |

## Queries that missed the top 5

- `q-artifact-size` "лимит размера артефакта": want k-artifact-limits, got k-grpc-auth
- `q-artifact-scheme` "можно ли file:// для артефактов": want k-artifact-https, got k-artifact-limits, k-redis-bus

_Generated 2026-10-13T07:43:40Z._
