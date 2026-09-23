#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export ADDR=127.0.0.1:8080
export SQLITE_PATH="$repo_dir/trial.db"

cd "$repo_dir"
exec go run ./cmd/server
