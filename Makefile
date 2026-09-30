.PHONY: up down test lint lint-stubs arch e2e run-api migrate sqlc

COMPOSE = docker compose -f deploy/compose.yaml
export CD_TEST_DATABASE_URL ?= postgres://postgres:postgres@127.0.0.1:7340/postgres?sslmode=disable

up:
	$(COMPOSE) up -d --wait

down:
	$(COMPOSE) down

migrate:
	cd go && go run ./cmd/cakerdesk migrate up

sqlc:
	cd go && sqlc generate

test:
	cd go && go test ./...
	cd kernel && uv run pytest
	cd mockllm && uv run pytest

lint:
	cd go && test -z "$$(gofmt -l .)"
	cd go && go vet ./...
	cd kernel && uv run ruff check .
	cd mockllm && uv run ruff check .
	$(MAKE) arch

lint-stubs:
	sh scripts/lint-stubs.sh

arch:
	cd go && go test ./internal/archtest/

e2e:
	@if [ -d tests/e2e ] && find tests/e2e -name 'test_*.py' | grep -q .; then \
		cd kernel && uv run pytest ../tests/e2e; \
	else \
		echo "no e2e tests in this step"; \
	fi

run-api:
	cd go && go run ./cmd/cakerdesk -role=api
