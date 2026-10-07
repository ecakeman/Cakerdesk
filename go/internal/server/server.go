package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"

	"cakerdesk/internal/store"
)

type Server struct {
	Store     *store.Store
	PythonURL string
	Workspace string
	WebDir    string

	mu   sync.Mutex
	subs map[string]map[chan store.Event]struct{}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects", s.createProject)
	mux.HandleFunc("GET /api/projects", s.listProjects)
	mux.HandleFunc("POST /api/projects/{projectID}/threads", s.createThread)
	mux.HandleFunc("GET /api/projects/{projectID}/threads", s.listThreads)
	mux.HandleFunc("POST /api/threads/{threadID}/runs", s.createRun)
	mux.HandleFunc("GET /api/runs/{runID}", s.getRun)
	mux.HandleFunc("GET /api/threads/{threadID}/messages", s.listMessages)
	mux.HandleFunc("GET /api/runs/{runID}/events", s.streamEvents)
	mux.HandleFunc("POST /internal/runs/{runID}/events", s.ingest)
	if s.WebDir != "" {
		mux.Handle("/", http.FileServer(http.Dir(s.WebDir)))
	}
	return mux
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	id := uuid.NewString()
	if err := s.Store.CreateProject(id, body.Name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "name": body.Name})
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListProjects()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []map[string]string{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createThread(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Title == "" {
		http.Error(w, "title required", http.StatusBadRequest)
		return
	}
	projectID := r.PathValue("projectID")
	id := uuid.NewString()
	if err := s.Store.CreateThread(id, projectID, body.Title); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	root := filepath.Join(s.Workspace, "threads", id)
	for _, name := range []string{"uploads", "work", "artifacts"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "title": body.Title})
}

func (s *Server) listThreads(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListThreads(r.PathValue("projectID"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []map[string]string{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Goal string `json:"goal"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Goal) == "" {
		http.Error(w, "goal required", http.StatusBadRequest)
		return
	}
	threadID := r.PathValue("threadID")
	projectID := r.URL.Query().Get("project_id")
	if projectID == "" {
		http.Error(w, "project_id required", http.StatusBadRequest)
		return
	}
	runID := uuid.NewString()
	run := store.Run{ID: runID, ProjectID: projectID, ThreadID: threadID, Goal: body.Goal, Status: "running"}
	if err := s.Store.CreateRun(run); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.Store.AddMessage(uuid.NewString(), threadID, "user", body.Goal); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	workspace := filepath.Join(s.Workspace, "threads", threadID)
	payload, _ := json.Marshal(map[string]string{
		"project_id":     projectID,
		"thread_id":      threadID,
		"run_id":         runID,
		"goal":           body.Goal,
		"workspace_root": workspace,
	})
	resp, err := http.Post(strings.TrimRight(s.PythonURL, "/")+"/internal/runs", "application/json", bytes.NewReader(payload))
	if err != nil {
		_ = s.Store.SetRunStatus(runID, "failed")
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		_ = s.Store.SetRunStatus(runID, "failed")
		http.Error(w, string(raw), http.StatusBadGateway)
		return
	}
	run.Status = "running"
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.Store.GetRun(r.PathValue("runID"))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListMessages(r.PathValue("threadID"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []map[string]string{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	var event store.Event
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		http.Error(w, "bad event", http.StatusBadRequest)
		return
	}
	if event.RunID == "" {
		event.RunID = r.PathValue("runID")
	}
	if event.RunID != r.PathValue("runID") || event.Seq <= 0 || event.Type == "" {
		http.Error(w, "bad event", http.StatusBadRequest)
		return
	}
	inserted, err := s.Store.Ingest(event)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if inserted {
		s.publish(event)
		s.projectMessage(event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "inserted": inserted})
}

func (s *Server) projectMessage(event store.Event) {
	run, err := s.Store.GetRun(event.RunID)
	if err != nil {
		return
	}
	switch event.Type {
	case "model.message":
		var payload struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(event.Payload, &payload)
		if payload.Content != "" {
			_ = s.Store.AddMessage(uuid.NewString(), run.ThreadID, "assistant", payload.Content)
		}
	case "tool.completed":
		var payload struct {
			Tool    string `json:"tool"`
			Status  string `json:"status"`
			Summary string `json:"summary"`
		}
		_ = json.Unmarshal(event.Payload, &payload)
		_ = s.Store.AddMessage(uuid.NewString(), run.ThreadID, "tool", payload.Tool+" "+payload.Status+" "+payload.Summary)
	}
}

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}
	after := int64(0)
	if raw := r.URL.Query().Get("after_seq"); raw != "" {
		after, _ = strconv.ParseInt(raw, 10, 64)
	} else if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		after, _ = strconv.ParseInt(raw, 10, 64)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	runID := r.PathValue("runID")
	existing, err := s.Store.EventsAfter(runID, after)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, event := range existing {
		writeSSE(w, event)
		after = event.Seq
	}
	flusher.Flush()
	ch := make(chan store.Event, 16)
	s.addSub(runID, ch)
	defer s.removeSub(runID, ch)
	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-ch:
			if event.Seq <= after {
				continue
			}
			writeSSE(w, event)
			after = event.Seq
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, event store.Event) {
	body, _ := json.Marshal(event)
	fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Seq, event.Type, body)
}

func (s *Server) addSub(runID string, ch chan store.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subs == nil {
		s.subs = map[string]map[chan store.Event]struct{}{}
	}
	if s.subs[runID] == nil {
		s.subs[runID] = map[chan store.Event]struct{}{}
	}
	s.subs[runID][ch] = struct{}{}
}

func (s *Server) removeSub(runID string, ch chan store.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subs[runID], ch)
}

func (s *Server) publish(event store.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs[event.RunID] {
		select {
		case ch <- event:
		default:
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
