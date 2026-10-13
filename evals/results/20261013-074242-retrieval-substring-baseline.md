# Memory retrieval eval — substring-baseline

Run `20261013-074242-retrieval-substring-baseline` · 30 queries over 30 entries · GET /memory/search path (KnowledgeStore.Search), top 10

| Metric | Value (95% CI, bootstrap over queries) |
|---|---|
| Recall@1 | 10.0% [0.0–23.3] (n=30) |
| Recall@3 | 10.0% [0.0–23.3] (n=30) |
| **Recall@5** | 10.0% [0.0–23.3] (n=30) |
| MRR@10 | 10.0% [0.0–23.3] (n=30) |
| Zero-result rate (lower is better) | 90.0% [76.7–100.0] (n=30) |
| Latency p95 | 0.5 ms |

## Recall@5 by query type

| Type | Recall@5 |
|---|---|
| en | 0.0% [0.0–0.0] (n=4) |
| exact | 100.0% [100.0–100.0] (n=2) |
| inflection | 0.0% [0.0–0.0] (n=13) |
| keyword | 33.3% [0.0–100.0] (n=3) |
| mixed | 0.0% [0.0–0.0] (n=4) |
| paraphrase | 0.0% [0.0–0.0] (n=8) |
| ru | 4.3% [0.0–13.0] (n=23) |

## Queries that missed the top 5

- `q-pool-size` "сколько соединений в пуле postgres": want k-pg-pool, got nothing
- `q-timestamps` "в каком типе хранится время": want k-pg-timestamptz, got nothing
- `q-two-verifiers` "как два верификатора не получают одно задание": want k-skip-locked, got nothing
- `q-key-revoke` "ротация ключей и отзыв": want k-rotation-race, got nothing
- `q-kafka-outage` "что будет если kafka упадёт": want k-kafka-down, k-outbox, got nothing
- `q-stream-cursor` "почему курсор стрима по seq": want k-watch-seq, got nothing
- `q-grpc-token` "как передать токен в grpc": want k-grpc-auth, got nothing
- `q-metrics-port` "на каком порту метрики": want k-metrics-port, got nothing
- `q-dashboard` "где дашборд графаны": want k-grafana, got nothing
- `q-slow-tests` "почему тесты медленные": want k-docker-tests, got nothing
- `q-parallel-tests` "параллельный запуск тестов": want k-test-schema, got nothing
- `q-injection` "защита от prompt injection": want k-injection-fence, got nothing
- `q-unsure-verdict` "что ставить верификатору если доказательств нет": want k-false-verify, got nothing
- `q-artifact-size` "лимит размера артефакта": want k-artifact-limits, got nothing
- `q-artifact-scheme` "можно ли file:// для артефактов": want k-artifact-https, got nothing
- `q-lease-extend` "как продлить аренду задачи": want k-lease, got nothing
- `q-verify-attempts` "сколько попыток проверки": want k-verify-retries, got nothing
- `q-retry-command` "команда telegram для повторной проверки": want k-telegram-retry, got nothing
- `q-not-found` "почему закрытый проект отвечает not_found": want k-closed-project, got nothing
- `q-register-limit` "лимит регистрации агентов": want k-ratelimit, got nothing
- `q-redis-role` "зачем нужен redis": want k-redis-bus, got nothing
- `q-longpoll-wait` "сколько ждёт long-poll": want k-longpoll, got nothing
- `q-offline-tests` "заглушка мозга для тестов без сети": want k-echo-brain, got nothing
- `q-en-order` "how is event order preserved": want k-en-relay-lock, got nothing
- `q-en-retry-create` "retrying task creation": want k-en-idempotent, got nothing
- `q-en-restart-counts` "metrics correct after restart": want k-en-scrape, got nothing
- `q-en-latency` "kafka consumer latency": want k-en-delay, got nothing

_Generated 2026-10-13T07:42:42Z._
