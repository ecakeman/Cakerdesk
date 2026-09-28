.PHONY: test lint lint-stubs e2e sabotage up down

export TESTCONTAINERS_RYUK_DISABLED ?= true

test:
	cd go && go test ./...

lint: lint-stubs
	@out=$$(cd go && gofmt -l .); if [ -n "$$out" ]; then echo "$$out"; exit 1; fi
	cd go && go vet ./...

lint-stubs:
	@! grep -RInE 'TODO|FIXME|XXX|HACK|not implemented|NotImplementedError|panic\("unimplemented"\)' \
		--include='*.go' --include='*.py' go mockllm \
		| grep -v '_test.go' \
		| grep -v '/vendor/'

e2e:
	cd tests/e2e && CAKERDESK_E2E_URL=$${CAKERDESK_E2E_URL:-http://127.0.0.1:7310} python3 -m pytest -q test_p0.py

sabotage:
	bash tests/sabotage/run.sh

up:
	docker compose -f deploy/compose.yaml up -d --build

down:
	docker compose -f deploy/compose.yaml down
