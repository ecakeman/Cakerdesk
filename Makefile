.PHONY: up down test lint lint-stubs arch e2e run-api

COMPOSE = docker compose -f deploy/compose.yaml

up:
	$(COMPOSE) up -d --wait

down:
	$(COMPOSE) down

test:
	cd go && go test ./...
	cd kernel && uv run pytest

lint:
	cd go && test -z "$$(gofmt -l .)"
	cd go && go vet ./...
	cd kernel && uv run ruff check .
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
