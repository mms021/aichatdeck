# Local Langfuse for evals

```bash
make langfuse        # UI: http://localhost:47400 (eval@example.test / local-eval-only-123)
LANGFUSE_HOST=http://localhost:47400 LANGFUSE_PUBLIC_KEY=pk-lf-local-eval \
  LANGFUSE_SECRET_KEY=sk-lf-local-eval go run ./cmd/eval retrieval
make langfuse-down
```

Upstream `docker-compose.yml` (Langfuse v4.58.0) plus
[compose.override.yml](compose.override.yml): pinned images, the project and
API keys pre-created, infra ports unpublished. MinIO's public images are no
longer pullable, so a local S3 mock stands in for it (`.env.s3` points
Langfuse at it); fine for evals, not for anything that must keep data.

Verified against this stack: eval runs appear under **Experiments** with item
counts and aggregated scores, and a model call shows as a generation with
usage and cost.
