package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/ecakeman/cakerdesk/internal/dbtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestHealthz(t *testing.T) {
	env := dbtest.New(t)
	srv := newTestServer(t, env)
	res := do(t, srv, http.MethodGet, "/healthz", "", nil)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "ok" {
		t.Fatalf("body %+v", body)
	}
}

func TestAuthRequired(t *testing.T) {
	env := dbtest.New(t)
	srv := newTestServer(t, env)
	res := do(t, srv, http.MethodGet, "/v1/agents", "", nil)
	assertError(t, res, http.StatusUnauthorized, "unauthorized")
	res = do(t, srv, http.MethodGet, "/v1/agents", "wrong", nil)
	assertError(t, res, http.StatusUnauthorized, "unauthorized")
}

func TestPublishIncrementsVersion(t *testing.T) {
	env := dbtest.New(t)
	srv := newTestServer(t, env)
	id := createAgent(t, srv, "inc-agent")
	v1 := publish(t, srv, id, validConfig("one"), http.StatusCreated)
	if v1 != 1 {
		t.Fatalf("v1=%d", v1)
	}
	v2 := publish(t, srv, id, validConfig("two"), http.StatusCreated)
	if v2 != 2 {
		t.Fatalf("v2=%d", v2)
	}
}

func TestPublishSameConfigIsIdempotent(t *testing.T) {
	env := dbtest.New(t)
	srv := newTestServer(t, env)
	id := createAgent(t, srv, "idem-agent")
	cfg := validConfig("same")
	a := publish(t, srv, id, cfg, http.StatusCreated)
	b := publish(t, srv, id, cfg, http.StatusOK)
	if a != 1 || b != 1 {
		t.Fatalf("a=%d b=%d", a, b)
	}
}

func TestPublishConcurrent(t *testing.T) {
	env := dbtest.New(t)
	srv := newTestServer(t, env)
	id := createAgent(t, srv, "conc-agent")
	var wg sync.WaitGroup
	got := make([]int, 10)
	errs := make([]error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res := do(t, srv, http.MethodPost, "/v1/agents/"+id+"/versions", "test-key", map[string]any{"config": validConfig(fmt.Sprintf("c%d", i))})
			defer res.Body.Close()
			if res.StatusCode != http.StatusCreated {
				b, _ := io.ReadAll(res.Body)
				errs[i] = fmt.Errorf("status %d %s", res.StatusCode, b)
				return
			}
			var body struct {
				Version int `json:"version"`
			}
			if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
				errs[i] = err
				return
			}
			got[i] = body.Version
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[int]struct{}{}
	for _, v := range got {
		if v < 1 || v > 10 {
			t.Fatalf("version %d", v)
		}
		if _, ok := seen[v]; ok {
			t.Fatalf("duplicate %d", v)
		}
		seen[v] = struct{}{}
	}
	if len(seen) != 10 {
		t.Fatalf("got %v", got)
	}
}

func TestConfigValidation(t *testing.T) {
	env := dbtest.New(t)
	srv := newTestServer(t, env)
	id := createAgent(t, srv, "val-agent")
	cases := []map[string]any{
		{"model": "m", "system_prompt": "x", "tools": []string{}},
		{"model": "m", "system_prompt": "x", "tools": []string{"submit_result", "submit_result"}},
		{"model": "m", "system_prompt": "x", "tools": []string{"bash"}},
		{"model": "m", "system_prompt": "x", "tools": []string{"list_files"}},
		{"model": "m", "system_prompt": "", "tools": []string{"submit_result"}},
		{"model": "m", "system_prompt": "x", "tools": []string{"submit_result"}, "limits": map[string]int{"max_llm_calls": 0, "max_tool_calls": 80, "max_total_tokens": 200000, "max_duration_s": 1800, "max_attempts": 3}},
		{"model": "m", "system_prompt": "x", "tools": []string{"submit_result"}, "context": map[string]int{"compact_threshold_tokens": 1, "keep_last_messages": 8}},
		{"model": "m", "system_prompt": "x", "tools": []string{"submit_result"}, "unknown_field": 1},
	}
	for i, cfg := range cases {
		res := do(t, srv, http.MethodPost, "/v1/agents/"+id+"/versions", "test-key", map[string]any{"config": cfg})
		assertError(t, res, http.StatusBadRequest, "invalid_config")
		_ = i
	}
}

func TestAgentVersionImmutable(t *testing.T) {
	env := dbtest.New(t)
	srv := newTestServer(t, env)
	id := createAgent(t, srv, "imm-agent")
	publish(t, srv, id, validConfig("v"), http.StatusCreated)
	_, err := env.App.Exec(context.Background(), "UPDATE agent_versions SET config_hash = 'x'")
	if err == nil {
		t.Fatal("expected permission denied")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("sqlstate %s err=%v", code, err)
	}
}

func TestDefaultPrivileges(t *testing.T) {
	env := dbtest.New(t)
	ctx := context.Background()
	if _, err := env.Migrate.Exec(ctx, "CREATE TABLE dp_probe (id int)"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.App.Exec(ctx, "SELECT id FROM dp_probe"); err != nil {
		t.Fatal(err)
	}
}

func newTestServer(t *testing.T, env *dbtest.Env) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(New(env.App, "test-key").Public)
	t.Cleanup(ts.Close)
	return ts
}

func createAgent(t *testing.T, srv *httptest.Server, name string) string {
	t.Helper()
	res := do(t, srv, http.MethodPost, "/v1/agents", "test-key", map[string]string{"name": name})
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("create %d %s", res.StatusCode, b)
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(body.ID); err != nil {
		t.Fatal(err)
	}
	return body.ID
}

func publish(t *testing.T, srv *httptest.Server, id string, cfg map[string]any, want int) int {
	t.Helper()
	res := do(t, srv, http.MethodPost, "/v1/agents/"+id+"/versions", "test-key", map[string]any{"config": cfg})
	defer res.Body.Close()
	if want != 0 && res.StatusCode != want {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("publish %d want %d %s", res.StatusCode, want, b)
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("publish %d %s", res.StatusCode, b)
	}
	var body struct {
		Version int `json:"version"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Version
}

func validConfig(prompt string) map[string]any {
	return map[string]any{
		"model":         "mock-1",
		"system_prompt": prompt,
		"tools":         []string{"submit_result"},
	}
}

func do(t *testing.T, srv *httptest.Server, method, path, key string, body any) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func assertError(t *testing.T, res *http.Response, status int, code string) {
	t.Helper()
	defer res.Body.Close()
	if res.StatusCode != status {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d want %d %s", res.StatusCode, status, b)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != code {
		t.Fatalf("code %s want %s", body.Error.Code, code)
	}
}

func pgCode(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code
	}
	return ""
}

func TestMain(m *testing.M) {
	if os.Getenv("CD_TEST_DATABASE_URL") == "" {
		os.Setenv("CD_TEST_DATABASE_URL", "postgres://postgres:postgres@127.0.0.1:7340/postgres?sslmode=disable")
	}
	os.Exit(m.Run())
}
