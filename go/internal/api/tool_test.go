package api

import (
	"net/http"
	"testing"

	"github.com/ecakeman/cakerdesk/internal/dbtest"
)

func TestToolCallRejectsUnknownTool(t *testing.T) {
	env := dbtest.New(t)
	pub, internal := newTestPair(t, env)
	id := queueRun(t, pub, "tool-unknown")
	res := do(t, internal, http.MethodPost, "/internal/runs/claim", "internal-test", map[string]any{"worker_id": "owner", "wait_ms": 0})
	decodeJSON(t, res, http.StatusOK)
	res = do(t, internal, http.MethodPost, "/internal/runs/"+id+"/tool-calls", "internal-test", map[string]any{
		"worker_id": "owner", "attempt": 1, "tool_call_id": "call-1", "name": "bash", "args": map[string]any{},
	})
	body := decodeJSON(t, res, http.StatusOK)
	if body["status"] != "failed" || body["error_code"] != "tool_not_in_agent" {
		t.Fatalf("%+v", body)
	}
}

func TestToolCallRequiresFence(t *testing.T) {
	env := dbtest.New(t)
	pub, internal := newTestPair(t, env)
	id := queueRun(t, pub, "tool-fence")
	call := map[string]any{
		"worker_id": "owner", "attempt": 1, "tool_call_id": "call-1", "name": "submit_result",
		"args": map[string]any{"summary": "ok"},
	}
	res := do(t, internal, http.MethodPost, "/internal/runs/"+id+"/tool-calls", "internal-test", call)
	assertError(t, res, http.StatusConflict, "lease_lost")

	res = do(t, internal, http.MethodPost, "/internal/runs/claim", "internal-test", map[string]any{"worker_id": "owner", "wait_ms": 0})
	decodeJSON(t, res, http.StatusOK)
	badAttempt := map[string]any{
		"worker_id": "owner", "attempt": 9, "tool_call_id": "call-1", "name": "submit_result",
		"args": map[string]any{"summary": "ok"},
	}
	res = do(t, internal, http.MethodPost, "/internal/runs/"+id+"/tool-calls", "internal-test", badAttempt)
	assertError(t, res, http.StatusConflict, "lease_lost")
	badWorker := map[string]any{
		"worker_id": "other", "attempt": 1, "tool_call_id": "call-1", "name": "submit_result",
		"args": map[string]any{"summary": "ok"},
	}
	res = do(t, internal, http.MethodPost, "/internal/runs/"+id+"/tool-calls", "internal-test", badWorker)
	assertError(t, res, http.StatusConflict, "lease_lost")

	res = do(t, internal, http.MethodPost, "/internal/runs/"+id+"/tool-calls", "internal-test", call)
	body := decodeJSON(t, res, http.StatusOK)
	if body["status"] != "succeeded" || body["output"] != "result accepted" {
		t.Fatalf("%+v", body)
	}
}
