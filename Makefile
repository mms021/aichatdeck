PG_PORT ?= 47432
export PG_PORT
TEST_DATABASE_URL ?= postgres://aichatdeck:aichatdeck@localhost:$(PG_PORT)/aichatdeck?sslmode=disable

.PHONY: up down test lint build proto evals langfuse langfuse-down

up:
	docker compose up -d --build

down:
	docker compose down

# Tests need only Postgres; each test gets its own schema.
test:
	docker compose up -d --wait postgres
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -race -count=1 ./...

lint:
	test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	go vet ./...

build:
	CGO_ENABLED=0 go build ./...

# Regenerate gRPC code from proto/ (needs buf, protoc-gen-go, protoc-gen-go-grpc).
proto:
	buf lint
	buf generate

# Offline evals: verifier harness check (oracle) and memory retrieval.
evals:
	docker compose up -d --wait postgres
	go run ./cmd/eval verifier -judge oracle -reps 1
	DATABASE_URL="$(TEST_DATABASE_URL)" go run ./cmd/eval retrieval

LANGFUSE_VERSION = v4.58.0

# Local Langfuse (see deploy/langfuse/README.md).
langfuse:
	curl -sfL https://raw.githubusercontent.com/langfuse/langfuse/$(LANGFUSE_VERSION)/docker-compose.yml -o deploy/langfuse/docker-compose.yml
	cd deploy/langfuse && docker compose -p aichatdeck-langfuse --env-file .env.s3 up -d

langfuse-down:
	cd deploy/langfuse && docker compose -p aichatdeck-langfuse down
