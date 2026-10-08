package server

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"cakerdesk/internal/cli"
	"cakerdesk/internal/db"
	"cakerdesk/internal/pythonclient"
)

func testServer(t *testing.T, python http.Handler) *httptest.Server {
	t.Helper()
	url := os.Getenv("CAKERDESK_DATABASE_URL")
	if url == "" {
		t.Skip("需要 CAKERDESK_DATABASE_URL，不退回 SQLite")
	}
	sqlDB, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	migrations := filepath.Join("..", "..", "migrations")
	if err := goose.Up(sqlDB, migrations); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec(`TRUNCATE events, runs, messages, threads, projects CASCADE`); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	py := httptest.NewServer(python)
	t.Cleanup(py.Close)
	srv := &Server{
		Q:         db.New(pool),
		Python:    pythonclient.Client{BaseURL: py.URL},
		Workspace: t.TempDir(),
	}
	return httptest.NewServer(srv.Router())
}

func TestRunAcceptsBeforePythonFinishesAndStoresEvents(t *testing.T) {
	var resumed bool
	api := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/internal/runs":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"run_id":"x","status":"accepted"}`))
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			w.WriteHeader(http.StatusAccepted)
		case strings.HasSuffix(r.URL.Path, "/resume"):
			resumed = true
			w.WriteHeader(http.StatusAccepted)
		case strings.HasSuffix(r.URL.Path, "/memory"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"kind":"lesson","content":"遗漏了结论"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	project := postJSON(t, api.URL+"/api/projects", `{"name":"周报"}`)
	thread := postJSON(t, api.URL+"/api/projects/"+project["id"].(string)+"/threads", `{"title":"销售"}`)
	body := `{"goal":"完成 artifacts/report.md"}`
	req, _ := http.NewRequest(http.MethodPost, api.URL+"/api/threads/"+thread["id"].(string)+"/runs?project_id="+project["id"].(string), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d %s", resp.StatusCode, raw)
	}
	var run map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		t.Fatal(err)
	}

	runID := run["id"].(string)
	resume, err := http.Post(api.URL+"/api/runs/"+runID+"/resume", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resume.Body.Close()
	if resume.StatusCode != http.StatusAccepted || !resumed {
		t.Fatalf("resume %d %v", resume.StatusCode, resumed)
	}
	events := []string{
		`{"run_id":"%s","seq":1,"type":"run.started","timestamp":"2026-10-07T00:00:00Z","payload":{"goal":"完成报告"}}`,
		`{"run_id":"%s","seq":2,"type":"tool.started","timestamp":"2026-10-07T00:00:01Z","payload":{"tool":"read_file"}}`,
		`{"run_id":"%s","seq":3,"type":"tool.completed","timestamp":"2026-10-07T00:00:02Z","payload":{"tool":"read_file","status":"completed","summary":"读取 notes"}}`,
		`{"run_id":"%s","seq":4,"type":"verification.completed","timestamp":"2026-10-07T00:00:03Z","payload":{"passed":false,"findings":[{"criterion":"结论","status":"failed","evidence":"缺少结论"}]}}`,
		`{"run_id":"%s","seq":5,"type":"run.completed","timestamp":"2026-10-07T00:00:04Z","payload":{"summary":"任务完成","artifacts":["artifacts/report.md"]}}`,
	}
	for _, pattern := range events {
		raw := []byte(sprintf(pattern, runID))
		res, err := http.Post(api.URL+"/internal/runs/"+runID+"/events", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("ingest %d", res.StatusCode)
		}
	}
	again, err := http.Post(api.URL+"/internal/runs/"+runID+"/events", "application/json", strings.NewReader(sprintf(events[0], runID)))
	if err != nil {
		t.Fatal(err)
	}
	var dup map[string]any
	_ = json.NewDecoder(again.Body).Decode(&dup)
	again.Body.Close()
	if dup["inserted"] != false {
		t.Fatalf("duplicate seq inserted: %#v", dup)
	}

	stream, err := http.Get(api.URL + "/api/runs/" + runID + "/events?after_seq=3")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	reader := bufio.NewReader(stream.Body)
	var text strings.Builder
	for text.Len() < 20 {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		text.WriteString(line)
		if strings.Contains(text.String(), "verification.completed") && strings.Contains(text.String(), "run.completed") {
			break
		}
	}
	if strings.Contains(text.String(), "tool.started") {
		t.Fatalf("after_seq leaked earlier event: %s", text.String())
	}
	if !strings.Contains(text.String(), "verification.completed") {
		t.Fatalf("history replay %#v", text.String())
	}
	var shown strings.Builder
	if err := cli.Execute([]string{"run", "show", "--run", runID}, cli.Options{BaseURL: api.URL, Out: &shown}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(shown.String(), `"status": "completed"`) && !strings.Contains(shown.String(), `"status":"completed"`) {
		t.Fatalf("cli show %#v", shown.String())
	}
}

func TestCancelPersistsWhenPythonIsDown(t *testing.T) {
	api := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/runs" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer api.Close()
	project := postJSON(t, api.URL+"/api/projects", `{"name":"周报"}`)
	thread := postJSON(t, api.URL+"/api/projects/"+project["id"].(string)+"/threads", `{"title":"销售"}`)
	req, _ := http.NewRequest(http.MethodPost, api.URL+"/api/threads/"+thread["id"].(string)+"/runs?project_id="+project["id"].(string), strings.NewReader(`{"goal":"写报告"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var run map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&run)
	resp.Body.Close()
	runID := run["id"].(string)
	cancel, err := http.Post(api.URL+"/api/runs/"+runID+"/cancel", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer cancel.Body.Close()
	if cancel.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(cancel.Body)
		t.Fatalf("cancel %d %s", cancel.StatusCode, raw)
	}
	statusResp, err := http.Get(api.URL + "/internal/runs/" + runID)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	_ = json.NewDecoder(statusResp.Body).Decode(&stored)
	statusResp.Body.Close()
	if stored["status"] != "cancel_requested" {
		t.Fatalf("status %#v", stored["status"])
	}
}

func postJSON(t *testing.T, url, body string) map[string]any {
	t.Helper()
	res, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func sprintf(pattern, id string) string {
	return strings.ReplaceAll(pattern, "%s", id)
}
