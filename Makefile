PG_PORT ?= 47432
export PG_PORT
TEST_DATABASE_URL ?= postgres://aichatdeck:aichatdeck@localhost:$(PG_PORT)/aichatdeck?sslmode=disable

.PHONY: up down test lint build

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
