package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	_ "modernc.org/sqlite"
)

type Event struct {
	RunID     string          `json:"run_id"`
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type Run struct {
	ID                   string `json:"id"`
	ProjectID            string `json:"project_id"`
	ThreadID             string `json:"thread_id"`
	Goal                 string `json:"goal"`
	Status               string `json:"status"`
	PlanSnapshot         string `json:"plan_snapshot"`
	VerificationSnapshot string `json:"verification_snapshot"`
	ArtifactPaths        string `json:"artifact_paths"`
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS threads (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  title TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS messages (
  id TEXT PRIMARY KEY,
  thread_id TEXT NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  thread_id TEXT NOT NULL,
  goal TEXT NOT NULL,
  status TEXT NOT NULL,
  plan_snapshot TEXT NOT NULL DEFAULT '',
  verification_snapshot TEXT NOT NULL DEFAULT '',
  artifact_paths TEXT NOT NULL DEFAULT '[]',
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
  run_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  type TEXT NOT NULL,
  timestamp TEXT NOT NULL,
  payload TEXT NOT NULL,
  PRIMARY KEY (run_id, seq)
);
CREATE TABLE IF NOT EXISTS artifact_meta (
  run_id TEXT NOT NULL,
  path TEXT NOT NULL,
  size INTEGER NOT NULL,
  mtime TEXT NOT NULL,
  PRIMARY KEY (run_id, path)
);
`)
	return err
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func (s *Store) CreateProject(id, name string) error {
	_, err := s.db.Exec(`INSERT INTO projects (id, name, created_at) VALUES (?, ?, ?)`, id, name, now())
	return err
}

func (s *Store) ListProjects() ([]map[string]string, error) {
	rows, err := s.db.Query(`SELECT id, name FROM projects ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out = append(out, map[string]string{"id": id, "name": name})
	}
	return out, rows.Err()
}

func (s *Store) CreateThread(id, projectID, title string) error {
	_, err := s.db.Exec(`INSERT INTO threads (id, project_id, title, created_at) VALUES (?, ?, ?, ?)`, id, projectID, title, now())
	return err
}

func (s *Store) ListThreads(projectID string) ([]map[string]string, error) {
	rows, err := s.db.Query(`SELECT id, title FROM threads WHERE project_id = ? ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, err
		}
		out = append(out, map[string]string{"id": id, "title": title})
	}
	return out, rows.Err()
}

func (s *Store) AddMessage(id, threadID, role, content string) error {
	_, err := s.db.Exec(`INSERT INTO messages (id, thread_id, role, content, created_at) VALUES (?, ?, ?, ?, ?)`, id, threadID, role, content, now())
	return err
}

func (s *Store) ListMessages(threadID string) ([]map[string]string, error) {
	rows, err := s.db.Query(`SELECT role, content FROM messages WHERE thread_id = ? ORDER BY created_at`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		var role, content string
		if err := rows.Scan(&role, &content); err != nil {
			return nil, err
		}
		out = append(out, map[string]string{"role": role, "content": content})
	}
	return out, rows.Err()
}

func (s *Store) CreateRun(run Run) error {
	_, err := s.db.Exec(`INSERT INTO runs (id, project_id, thread_id, goal, status, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		run.ID, run.ProjectID, run.ThreadID, run.Goal, run.Status, now())
	return err
}

func (s *Store) GetRun(id string) (Run, error) {
	var run Run
	err := s.db.QueryRow(`SELECT id, project_id, thread_id, goal, status, plan_snapshot, verification_snapshot, artifact_paths FROM runs WHERE id = ?`, id).
		Scan(&run.ID, &run.ProjectID, &run.ThreadID, &run.Goal, &run.Status, &run.PlanSnapshot, &run.VerificationSnapshot, &run.ArtifactPaths)
	return run, err
}

func (s *Store) SetRunStatus(id, status string) error {
	_, err := s.db.Exec(`UPDATE runs SET status = ? WHERE id = ?`, status, id)
	return err
}

func (s *Store) Ingest(event Event) (bool, error) {
	res, err := s.db.Exec(
		`INSERT INTO events (run_id, seq, type, timestamp, payload) VALUES (?, ?, ?, ?, ?) ON CONFLICT (run_id, seq) DO NOTHING`,
		event.RunID, event.Seq, event.Type, event.Timestamp, string(event.Payload),
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	switch event.Type {
	case "plan.updated":
		_, err = s.db.Exec(`UPDATE runs SET plan_snapshot = ? WHERE id = ?`, string(event.Payload), event.RunID)
	case "verification.completed":
		_, err = s.db.Exec(`UPDATE runs SET verification_snapshot = ? WHERE id = ?`, string(event.Payload), event.RunID)
	case "run.completed":
		_, err = s.db.Exec(`UPDATE runs SET status = ?, artifact_paths = ? WHERE id = ?`, "completed", artifactPaths(event.Payload), event.RunID)
	case "run.failed":
		_, err = s.db.Exec(`UPDATE runs SET status = ? WHERE id = ?`, "failed", event.RunID)
	case "run.cancelled":
		_, err = s.db.Exec(`UPDATE runs SET status = ? WHERE id = ?`, "cancelled", event.RunID)
	}
	return true, err
}

func artifactPaths(payload json.RawMessage) string {
	var body struct {
		Artifacts []string `json:"artifacts"`
	}
	if err := json.Unmarshal(payload, &body); err != nil || body.Artifacts == nil {
		return "[]"
	}
	raw, _ := json.Marshal(body.Artifacts)
	return string(raw)
}

func (s *Store) EventsAfter(runID string, after int64) ([]Event, error) {
	rows, err := s.db.Query(`SELECT run_id, seq, type, timestamp, payload FROM events WHERE run_id = ? AND seq > ? ORDER BY seq`, runID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var event Event
		var payload string
		if err := rows.Scan(&event.RunID, &event.Seq, &event.Type, &event.Timestamp, &payload); err != nil {
			return nil, err
		}
		event.Payload = json.RawMessage(payload)
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *Store) UpsertArtifact(runID, path string, size int64, mtime string) error {
	_, err := s.db.Exec(`INSERT INTO artifact_meta (run_id, path, size, mtime) VALUES (?, ?, ?, ?)
		ON CONFLICT (run_id, path) DO UPDATE SET size = excluded.size, mtime = excluded.mtime`, runID, path, size, mtime)
	return err
}

var ErrNotFound = errors.New("not found")
