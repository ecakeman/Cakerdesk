#!/bin/bash
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT

run_case() {
  name=$1
  shift
  echo "== sabotage $name =="
  rm -rf "$tmp/tree"
  mkdir -p "$tmp/tree"
  tar -C "$root" --exclude .git --exclude go/bin -cf - . | tar -C "$tmp/tree" -xf -
  (cd "$tmp/tree" && "$@")
}

run_case rls python3 - <<'PY'
from pathlib import Path
p = Path("go/db/migrations/0005_skills_rls.sql")
text = p.read_text()
old = "tenant_id = current_setting(''cd.tenant_id'', true)::uuid"
if old not in text:
    raise SystemExit("policy predicate missing")
p.write_text(text.replace(old, "true"))
PY
set +e
(cd "$tmp/tree/go" && TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/integration/ -count=1 -run TestT_rls -timeout 180s)
rls_status=$?
set -e
if [ "$rls_status" -eq 0 ]; then
  echo "T-rls still passed after RLS policy was opened"
  exit 1
fi

run_case routes python3 - <<'PY'
from pathlib import Path
p = Path("go/internal/httpapi/routes.go")
text = p.read_text()
needle = 's.Public.GET("/v1/agents", s.requirePerm(authsvc.PermAgentRead), s.listAgents)'
insert = 's.Public.GET("/v1/agents", s.listAgents)'
if needle not in text:
    raise SystemExit("route line missing")
p.write_text(text.replace(needle, insert, 1))
PY
set +e
(cd "$tmp/tree/go" && go test ./internal/httpapi/ -count=1 -run TestT_routes_perm)
routes_status=$?
set -e
if [ "$routes_status" -eq 0 ]; then
  echo "T-routes-perm still passed after requirePerm was removed"
  exit 1
fi
echo "sabotage ok"
