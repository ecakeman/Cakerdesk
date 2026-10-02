package api

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ecakeman/cakerdesk/internal/dbtest"
	"github.com/google/uuid"
)

func TestCreateRunQueued(t *testing.T) {
	env := dbtest.New(t)
	dir := t.TempDir()
	srv := newTestServerIn(t, env, dir)
	id := createAgent(t, srv, "run-agent")
	res := do(t, srv, http.MethodPost, "/v1/sessions", "test-key", map[string]string{"agent_id": id})
	assertError(t, res, http.StatusConflict, "agent_unpublished")

	publish(t, srv, id, validConfig("snap"), http.StatusCreated)
	res = do(t, srv, http.MethodPost, "/v1/sessions", "test-key", map[string]string{"agent_id": id, "title": "t"})
	sess := decodeJSON(t, res, http.StatusCreated)
	ws := filepath.Join(dir, sess["id"].(string))
	info, err := os.Stat(ws)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}

	res = do(t, srv, http.MethodPost, "/v1/sessions/"+sess["id"].(string)+"/runs", "test-key", map[string]string{"input": "hello"})
	run := decodeJSON(t, res, http.StatusCreated)
	if run["status"] != "queued" || run["attempt"] != float64(0) {
		t.Fatalf("run %+v", run)
	}
	res = do(t, srv, http.MethodGet, "/v1/runs/"+run["id"].(string)+"/config", "test-key", nil)
	got := decodeJSON(t, res, http.StatusOK)
	res = do(t, srv, http.MethodGet, "/v1/agents/"+id+"/versions/1", "test-key", nil)
	ver := decodeJSON(t, res, http.StatusOK)
	if !jsonEqual(got["config"], ver["config"]) {
		t.Fatalf("config %s want %s", mustJSON(got["config"]), mustJSON(ver["config"]))
	}
	res = do(t, srv, http.MethodGet, "/v1/sessions/"+sess["id"].(string), "test-key", nil)
	view := decodeJSON(t, res, http.StatusOK)
	if view["active_run_id"] != run["id"] {
		t.Fatalf("active %v", view["active_run_id"])
	}
}

func TestOneActiveRunPerSession(t *testing.T) {
	env := dbtest.New(t)
	srv := newTestServer(t, env)
	id := createAgent(t, srv, "busy-agent")
	publish(t, srv, id, validConfig("busy"), http.StatusCreated)
	res := do(t, srv, http.MethodPost, "/v1/sessions", "test-key", map[string]string{"agent_id": id})
	sess := decodeJSON(t, res, http.StatusCreated)
	path := "/v1/sessions/" + sess["id"].(string) + "/runs"

	var wg sync.WaitGroup
	codes := make([]int, 20)
	bodies := make([]string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(`{"input":"x"}`))
			if err != nil {
				codes[i] = -1
				bodies[i] = err.Error()
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer test-key")
			res, err := http.DefaultClient.Do(req)
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
	var created int
	var runID string
	for i, code := range codes {
		if code == http.StatusCreated {
			created++
			var body map[string]any
			if err := json.Unmarshal([]byte(bodies[i]), &body); err != nil {
				t.Fatal(err)
			}
			runID = body["id"].(string)
		} else if code != http.StatusConflict {
			t.Fatalf("status %d %s", code, bodies[i])
		}
	}
	if created != 1 {
		t.Fatalf("created %d", created)
	}
	for i, code := range codes {
		if code != http.StatusConflict {
			continue
		}
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(bodies[i]), &body); err != nil {
			t.Fatal(err)
		}
		if body.Error.Code != "session_busy" || !strings.Contains(body.Error.Message, runID) {
			t.Fatalf("conflict %s", bodies[i])
		}
	}
}

func TestRunSnapshotsConfig(t *testing.T) {
	env := dbtest.New(t)
	srv := newTestServer(t, env)
	id := createAgent(t, srv, "snap-agent")
	publish(t, srv, id, validConfig("v1"), http.StatusCreated)
	res := do(t, srv, http.MethodPost, "/v1/sessions", "test-key", map[string]string{"agent_id": id})
	sess := decodeJSON(t, res, http.StatusCreated)
	res = do(t, srv, http.MethodPost, "/v1/sessions/"+sess["id"].(string)+"/runs", "test-key", map[string]string{"input": "keep"})
	run := decodeJSON(t, res, http.StatusCreated)
	res = do(t, srv, http.MethodGet, "/v1/runs/"+run["id"].(string)+"/config", "test-key", nil)
	before := decodeJSON(t, res, http.StatusOK)
	publish(t, srv, id, validConfig("v2"), http.StatusCreated)
	res = do(t, srv, http.MethodGet, "/v1/runs/"+run["id"].(string), "test-key", nil)
	after := decodeJSON(t, res, http.StatusOK)
	if after["agent_version"] != float64(1) {
		t.Fatalf("version %v", after["agent_version"])
	}
	res = do(t, srv, http.MethodGet, "/v1/runs/"+run["id"].(string)+"/config", "test-key", nil)
	got := decodeJSON(t, res, http.StatusOK)
	if !jsonEqual(before["config"], got["config"]) {
		t.Fatal("config changed")
	}
}

func TestRunStatusInvariants(t *testing.T) {
	env := dbtest.New(t)
	srv := newTestServer(t, env)
	id := createAgent(t, srv, "inv-agent")
	publish(t, srv, id, validConfig("inv"), http.StatusCreated)
	res := do(t, srv, http.MethodPost, "/v1/sessions", "test-key", map[string]string{"agent_id": id})
	sess := decodeJSON(t, res, http.StatusCreated)
	_, err := env.App.Exec(t.Context(), `
		INSERT INTO runs (session_id, agent_id, agent_version, config, input, status, max_attempts)
		VALUES ($1, $2, 1, '{}', 'x', 'running', 3)`,
		uuid.MustParse(sess["id"].(string)), uuid.MustParse(id))
	if pgCode(err) != "23514" {
		t.Fatalf("code %s err %v", pgCode(err), err)
	}
}

func decodeJSON(t *testing.T, res *http.Response, status int) map[string]any {
	t.Helper()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != status {
		t.Fatalf("status %d want %d %s", res.StatusCode, status, b)
	}
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func jsonEqual(a, b any) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(ab) == string(bb)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
