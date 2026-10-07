package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cakerdesk/internal/store"
)

func testServer(t *testing.T, python http.Handler) *httptest.Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "cakerdesk.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	py := httptest.NewServer(python)
	t.Cleanup(py.Close)
	srv := &Server{
		Store:     st,
		PythonURL: py.URL,
		Workspace: t.TempDir(),
	}
	return httptest.NewServer(srv.Handler())
}

func TestRunAcceptsBeforePythonFinishesAndStoresEvents(t *testing.T) {
	api := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/runs" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"run_id":"x","status":"accepted"}`))
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
	got, err := http.Get(api.URL + "/api/runs/" + runID)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	_ = json.NewDecoder(got.Body).Decode(&stored)
	got.Body.Close()
	if stored["status"] != "completed" {
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
