package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ecakeman/cakerdesk/internal/dbtest"
)

func TestClaimSkipLocked(t *testing.T) {
	env := dbtest.New(t)
	pub, internal := newTestPair(t, env)
	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, queueRun(t, pub, fmt.Sprintf("skip-%d", i)))
	}
	var wg sync.WaitGroup
	codes := make([]int, 10)
	bodies := make([]string, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := postJSON(internal.URL+"/internal/runs/claim", "internal-test", map[string]any{"worker_id": "w", "wait_ms": 0})
			if err != nil {
				codes[i] = -1
				bodies[i] = err.Error()
				return
			}
			defer res.Body.Close()
			b, _ := io.ReadAll(res.Body)
			codes[i] = res.StatusCode
			bodies[i] = string(b)
		}(i)
	}
	wg.Wait()
	got := map[string]struct{}{}
	var ok, empty int
	for i, code := range codes {
		switch code {
		case http.StatusOK:
			ok++
			var body map[string]any
			if err := json.Unmarshal([]byte(bodies[i]), &body); err != nil {
				t.Fatal(err)
			}
			got[body["run_id"].(string)] = struct{}{}
		case http.StatusNoContent:
			empty++
		default:
			t.Fatalf("status %d %s", code, bodies[i])
		}
	}
	if ok != 5 || empty != 5 || len(got) != 5 {
		t.Fatalf("ok %d empty %d ids %d", ok, empty, len(got))
	}
	for _, id := range ids {
		if _, ok := got[id]; !ok {
			t.Fatalf("missing %s", id)
		}
	}
}

func TestClaimFIFO(t *testing.T) {
	env := dbtest.New(t)
	pub, internal := newTestPair(t, env)
	first := queueRun(t, pub, "fifo-a")
	second := queueRun(t, pub, "fifo-b")
	res := do(t, internal, http.MethodPost, "/internal/runs/claim", "internal-test", map[string]any{"worker_id": "w", "wait_ms": 0})
	body := decodeJSON(t, res, http.StatusOK)
	if body["run_id"] != first {
		t.Fatalf("run %v want %s", body["run_id"], first)
	}
	if body["attempt"] != float64(1) || body["lease_seconds"] != float64(30) || body["heartbeat_seconds"] != float64(10) {
		t.Fatalf("claim %+v", body)
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools %+v", body["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "submit_result" || tool["description"] == "" || tool["parameters"] == nil {
		t.Fatalf("tool %+v", tool)
	}
	res = do(t, internal, http.MethodPost, "/internal/runs/claim", "internal-test", map[string]any{"worker_id": "w", "wait_ms": 0})
	body = decodeJSON(t, res, http.StatusOK)
	if body["run_id"] != second {
		t.Fatalf("run %v want %s", body["run_id"], second)
	}
}

func TestClaimLongPollWakes(t *testing.T) {
	env := dbtest.New(t)
	pub, internal := newTestPair(t, env)
	type result struct {
		status int
		body   string
		at     time.Time
	}
	ch := make(chan result, 1)
	go func() {
		res, err := postJSON(internal.URL+"/internal/runs/claim", "internal-test", map[string]any{"worker_id": "w", "wait_ms": 5000})
		if err != nil {
			ch <- result{status: -1, body: err.Error(), at: time.Now()}
			return
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		ch <- result{status: res.StatusCode, body: string(b), at: time.Now()}
	}()
	time.Sleep(time.Second)
	id := queueRun(t, pub, "wake")
	inserted := time.Now()
	select {
	case got := <-ch:
		if got.at.Sub(inserted) > 2*time.Second {
			t.Fatalf("late %s", got.at.Sub(inserted))
		}
		if got.status != http.StatusOK {
			t.Fatalf("status %d %s", got.status, got.body)
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(got.body), &body); err != nil {
			t.Fatal(err)
		}
		if body["run_id"] != id {
			t.Fatalf("run %v", body["run_id"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("claim did not return")
	}
}

func TestClaimEmptyReturns204(t *testing.T) {
	env := dbtest.New(t)
	_, internal := newTestPair(t, env)
	res := do(t, internal, http.MethodPost, "/internal/runs/claim", "internal-test", map[string]any{"worker_id": "w", "wait_ms": 0})
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d %s", res.StatusCode, b)
	}
}

func TestCompleteRequiresFence(t *testing.T) {
	env := dbtest.New(t)
	pub, internal := newTestPair(t, env)
	id := queueRun(t, pub, "fence")
	res := do(t, internal, http.MethodPost, "/internal/runs/claim", "internal-test", map[string]any{"worker_id": "owner", "wait_ms": 0})
	claim := decodeJSON(t, res, http.StatusOK)
	if claim["run_id"] != id {
		t.Fatalf("claimed %v", claim["run_id"])
	}
	res = do(t, internal, http.MethodPost, "/internal/runs/"+id+"/complete", "internal-test", map[string]any{
		"worker_id": "owner", "attempt": 99, "status": "succeeded",
	})
	assertError(t, res, http.StatusConflict, "lease_lost")
	res = do(t, internal, http.MethodPost, "/internal/runs/"+id+"/complete", "internal-test", map[string]any{
		"worker_id": "other", "attempt": 1, "status": "succeeded",
	})
	assertError(t, res, http.StatusConflict, "lease_lost")
	res = do(t, internal, http.MethodPost, "/internal/runs/"+id+"/complete", "internal-test", map[string]any{
		"worker_id": "owner", "attempt": 1, "status": "succeeded", "result": map[string]any{"ok": true},
	})
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("complete %d %s", res.StatusCode, b)
	}
	res = do(t, internal, http.MethodPost, "/internal/runs/"+id+"/complete", "internal-test", map[string]any{
		"worker_id": "owner", "attempt": 1, "status": "succeeded",
	})
	assertError(t, res, http.StatusConflict, "lease_lost")
}

func TestInternalAPIRequiresToken(t *testing.T) {
	env := dbtest.New(t)
	_, internal := newTestPair(t, env)
	res := do(t, internal, http.MethodPost, "/internal/runs/claim", "", map[string]any{"worker_id": "w", "wait_ms": 0})
	assertError(t, res, http.StatusUnauthorized, "unauthorized")
	res = do(t, internal, http.MethodPost, "/internal/runs/claim", "test-key", map[string]any{"worker_id": "w", "wait_ms": 0})
	assertError(t, res, http.StatusUnauthorized, "unauthorized")
}

func newTestPair(t *testing.T, env *dbtest.Env) (pub, internal *httptest.Server) {
	t.Helper()
	app := New(env.App, Options{
		APIKey:           "test-key",
		WorkspacesDir:    t.TempDir(),
		InternalToken:    "internal-test",
		LeaseSeconds:     30,
		HeartbeatSeconds: 10,
		ClaimMaxWait:     20 * time.Second,
	})
	pub = httptest.NewServer(app.Public)
	internal = httptest.NewServer(app.Internal)
	t.Cleanup(pub.Close)
	t.Cleanup(internal.Close)
	return pub, internal
}

func queueRun(t *testing.T, pub *httptest.Server, name string) string {
	t.Helper()
	id := createAgent(t, pub, name)
	publish(t, pub, id, validConfig("q"), http.StatusCreated)
	res := do(t, pub, http.MethodPost, "/v1/sessions", "test-key", map[string]string{"agent_id": id})
	sess := decodeJSON(t, res, http.StatusCreated)
	res = do(t, pub, http.MethodPost, "/v1/sessions/"+sess["id"].(string)+"/runs", "test-key", map[string]string{"input": "go"})
	run := decodeJSON(t, res, http.StatusCreated)
	return run["id"].(string)
}

func postJSON(url, key string, body any) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return http.DefaultClient.Do(req)
}
