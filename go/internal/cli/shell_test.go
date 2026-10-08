package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestShellUsesOnlyProjectAndSpeaksTheTimeline(t *testing.T) {
	var goal string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/projects" && r.Method == http.MethodGet:
			fmt.Fprint(w, `[{"id":"p1","name":"weekly-report"}]`)
		case r.URL.Path == "/api/projects/p1/threads" && r.Method == http.MethodGet:
			fmt.Fprint(w, `[{"id":"t1","title":"周报"}]`)
		case r.URL.Path == "/api/threads/t1/runs" && r.Method == http.MethodPost:
			var body struct {
				Goal string `json:"goal"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			goal = body.Goal
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"id":"run-1","status":"running"}`)
		case r.URL.Path == "/api/runs/run-1/events":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "id: 1\nevent: plan.updated\ndata: {\"type\":\"plan.updated\",\"payload\":{\"plan\":{\"steps\":[{\"id\":\"s1\",\"title\":\"读取 notes.txt\",\"status\":\"pending\"}]}}}\n\n")
			fmt.Fprint(w, "id: 2\nevent: verification.completed\ndata: {\"type\":\"verification.completed\",\"payload\":{\"passed\":false,\"findings\":[{\"criterion\":\"结论\",\"status\":\"failed\",\"evidence\":\"缺少结论\"}]}}\n\n")
			fmt.Fprint(w, "id: 3\nevent: run.completed\ndata: {\"type\":\"run.completed\",\"payload\":{\"summary\":\"任务完成\",\"artifacts\":[\"artifacts/report.md\"]}}\n\n")
		case r.URL.Path == "/api/projects/p1/memory":
			fmt.Fprint(w, `[{"kind":"lesson","content":"缺结论会被退回"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	input := strings.NewReader("根据 sales.csv 完成报告\n/memory\n/exit\n")
	var out strings.Builder
	err := Shell(Options{BaseURL: server.URL, Out: &out, In: input})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if goal != "根据 sales.csv 完成报告" {
		t.Fatalf("goal %#v", goal)
	}
	for _, want := range []string{"Project: weekly-report", "Thread: t1", "plan.updated s1 读取 notes.txt pending", "verification.completed 未通过 结论", "run.completed 任务完成 artifacts/report.md", "lesson: 缺结论会被退回"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in %s", want, text)
		}
	}
	if done := strings.Index(text, "run.completed 任务完成"); done < 0 || !strings.Contains(text[done:], "\n> ") {
		t.Fatalf("prompt did not return after the run: %s", text)
	}
	if strings.Contains(text, "data:") || strings.Contains(text, `"payload"`) {
		t.Fatalf("raw protocol in %s", text)
	}
}

func TestShellCreatesTheOnlyMissingProject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/projects" && r.Method == http.MethodGet:
			fmt.Fprint(w, `[]`)
		case r.URL.Path == "/api/projects" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"p9","name":"weekly-report"}`)
		case r.URL.Path == "/api/projects/p9/threads" && r.Method == http.MethodGet:
			fmt.Fprint(w, `[]`)
		case r.URL.Path == "/api/projects/p9/threads" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"t9","title":"主线"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	input := strings.NewReader("weekly-report\n主线\n/exit\n")
	var out strings.Builder
	if err := Shell(Options{BaseURL: server.URL, Out: &out, In: input}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "Project: weekly-report") || !strings.Contains(text, "Thread: t9") {
		t.Fatalf("%s", text)
	}
}
