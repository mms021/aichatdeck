#!/usr/bin/env bash
# Local server for agents configured in .mcp.json: Postgres, Redis and Kafka from
# docker compose, the server itself from source on 127.0.0.1:47080.
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_dir"
docker compose up -d --wait postgres redis kafka

export ADDR=127.0.0.1:47080
export DATABASE_URL="${DATABASE_URL:-postgres://aichatdeck:aichatdeck@localhost:${PG_PORT:-47432}/aichatdeck?sslmode=disable}"
exec go run ./cmd/server
