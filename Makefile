ROOT := $(abspath .)
PY := $(ROOT)/.venv/bin/python
export CAKERDESK_WORKSPACE := $(ROOT)/workspace
export CAKERDESK_MIGRATIONS := $(ROOT)/go/migrations
export CAKERDESK_LISTEN ?= :8080
export PORT ?= 8090
export CAKERDESK_URL ?= http://127.0.0.1:8080
export CAKERDESK_GO_URL ?= http://127.0.0.1:8080
export CAKERDESK_PYTHON_URL ?= http://127.0.0.1:8090

.PHONY: help setup dev shell test fmt

help:
	@printf '%s\n' \
		'setup  创建 .venv 并安装 Python 依赖，下载 Go 模块' \
		'dev    启动 Go :8080 和 Python :8090，然后进入 Shell' \
		'shell  只进入 Shell' \
		'test   go test 与 pytest。没有数据库时相关测试跳过' \
		'fmt    gofmt 与 python -m compileall'

setup:
	uv venv .venv --python 3.12
	UV_INDEX_URL=https://mirrors.aliyun.com/pypi/simple uv pip install --python $(PY) -r python/requirements.txt
	cd go && go mod download

dev:
	@test -x $(PY) || { echo "缺少 .venv，先执行 make setup"; exit 1; }
	@set -a; [ -f .env ] && . ./.env; set +a; \
	export CAKERDESK_WORKSPACE="$(CAKERDESK_WORKSPACE)"; \
	export CAKERDESK_MIGRATIONS="$(CAKERDESK_MIGRATIONS)"; \
	export CAKERDESK_LISTEN="$${CAKERDESK_LISTEN:-:8080}"; \
	export PORT="$${PORT:-8090}"; \
	export CAKERDESK_URL="$${CAKERDESK_URL:-http://127.0.0.1:8080}"; \
	export CAKERDESK_GO_URL="$${CAKERDESK_GO_URL:-http://127.0.0.1:8080}"; \
	export CAKERDESK_PYTHON_URL="$${CAKERDESK_PYTHON_URL:-http://127.0.0.1:8090}"; \
	free_port() { \
	  pids=$$(ss -H -ltnp "sport = :$$1" 2>/dev/null | sed -n 's/.*pid=\([0-9][0-9]*\).*/\1/p' | sort -u); \
	  if [ -n "$$pids" ]; then kill $$pids 2>/dev/null || true; sleep 0.3; fi; \
	}; \
	free_port 8080; \
	free_port 8090; \
	free_port 3000; \
	test -d $(ROOT)/frontend/node_modules || npm --prefix $(ROOT)/frontend install; \
	( cd $(ROOT)/go && go build -o $(ROOT)/.cakerdesk-dev ./cmd/cakerdesk ); \
	$(PY) -c 'import os,sys; os.setsid(); os.execvp(sys.argv[1], sys.argv[1:])' $(ROOT)/.cakerdesk-dev serve & go_pid=$$!; \
	$(PY) -c 'import os,sys; os.chdir(sys.argv[1]); os.setsid(); os.execvp(sys.argv[2], sys.argv[2:])' $(ROOT)/python $(PY) -m cakerdesk.main & py_pid=$$!; \
	npm --prefix $(ROOT)/frontend run dev & web_pid=$$!; \
	trap 'kill $$go_pid $$py_pid $$web_pid 2>/dev/null || true; wait $$go_pid $$py_pid $$web_pid 2>/dev/null || true' EXIT INT TERM; \
	i=0; \
	until curl -sf "$$CAKERDESK_URL/healthz" >/dev/null; do \
		i=$$((i+1)); \
		if [ $$i -gt 90 ]; then echo "服务没有起来"; exit 1; fi; \
		sleep 1; \
	done; \
	echo "Go: ready"; \
	i=0; \
	until curl -sf "$$CAKERDESK_PYTHON_URL/healthz" >/dev/null; do \
		i=$$((i+1)); \
		if [ $$i -gt 90 ]; then echo "服务没有起来"; exit 1; fi; \
		sleep 1; \
	done; \
	echo "Python: ready"; \
	i=0; \
	until curl -sf http://127.0.0.1:3000 >/dev/null; do \
		i=$$((i+1)); \
		if [ $$i -gt 90 ]; then echo "工作区没有起来"; exit 1; fi; \
		sleep 1; \
	done; \
	echo "工作区: http://127.0.0.1:3000"; \
	wait $$web_pid

test:
	cd go && go test ./...
	cd python && $(PY) -m pytest -q

fmt:
	gofmt -w go
	$(PY) -m compileall -q python/cakerdesk
