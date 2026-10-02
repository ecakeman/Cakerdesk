package api

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ecakeman/cakerdesk/internal/apperr"
	"github.com/ecakeman/cakerdesk/internal/dbtest"
	"github.com/ecakeman/cakerdesk/internal/events"
	"github.com/google/uuid"
)

func TestSeqMonotonicConcurrent(t *testing.T) {
	env := dbtest.New(t)
	pub, _ := newTestPair(t, env)
	id := queueRun(t, pub, "seq")
	if _, err := env.App.Exec(t.Context(), `DELETE FROM run_events WHERE run_id = $1`, uuid.MustParse(id)); err != nil {
		t.Fatal(err)
	}
	if _, err := env.App.Exec(t.Context(), `UPDATE runs SET last_seq = 0 WHERE id = $1`, uuid.MustParse(id)); err != nil {
		t.Fatal(err)
	}
	ev := events.New(env.App, "")
	var wg sync.WaitGroup
	seqs := make([]int64, 50)
	errs := make([]error, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, last, err := ev.Append(context.Background(), uuid.MustParse(id), []events.Event{{
				Type: "message.assistant", Attempt: 0, Payload: []byte(`{"content":"x","tool_calls":[],"usage":{}}`),
			}}, nil)
			seqs[i] = last
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for i, seq := range seqs {
		if seq != int64(i+1) {
			t.Fatalf("seqs %v", seqs)
		}
	}
}

func TestAppendFencedRejectsStaleAttempt(t *testing.T) {
	env := dbtest.New(t)
	pub, internal := newTestPair(t, env)
	id := queueRun(t, pub, "fence-ev")
	res := do(t, internal, http.MethodPost, "/internal/runs/claim", "internal-test", map[string]any{"worker_id": "owner", "wait_ms": 0})
	decodeJSON(t, res, http.StatusOK)
	var before int64
	if err := env.App.QueryRow(t.Context(), `SELECT last_seq FROM runs WHERE id = $1`, uuid.MustParse(id)).Scan(&before); err != nil {
		t.Fatal(err)
	}
	ev := events.New(env.App, "")
	_, _, err := ev.Append(t.Context(), uuid.MustParse(id), []events.Event{{
		Type: "message.assistant", Attempt: 99, Payload: []byte(`{}`),
	}}, &events.Fence{WorkerID: "owner", Attempt: 99})
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != "lease_lost" {
		t.Fatal(err)
	}
	var after int64
	if err := env.App.QueryRow(t.Context(), `SELECT last_seq FROM runs WHERE id = $1`, uuid.MustParse(id)).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("last_seq %d want %d", after, before)
	}
}

func TestSSEReplayThenLive(t *testing.T) {
	env := dbtest.New(t)
	pub, internal := newTestPair(t, env)
	id := resetEvents(t, env, queueRun(t, pub, "sse-live"))
	ev := events.New(env.App, "redis://127.0.0.1:7341/0")
	appendN(t, ev, id, 3)
	body, cancel := openSSE(t, pub, "/v1/runs/"+id+"/events/stream", "")
	defer cancel()
	got := readSSE(t, body, 3)
	appendN(t, ev, id, 2)
	got = append(got, readSSE(t, body, 2)...)
	assertIDs(t, got, []string{"1", "2", "3", "4", "5"})
	_ = internal
}

func TestSSELastEventID(t *testing.T) {
	env := dbtest.New(t)
	pub, _ := newTestPair(t, env)
	id := resetEvents(t, env, queueRun(t, pub, "sse-last"))
	ev := events.New(env.App, "redis://127.0.0.1:7341/0")
	appendN(t, ev, id, 5)
	body, cancel := openSSE(t, pub, "/v1/runs/"+id+"/events/stream", "3")
	defer cancel()
	got := readSSE(t, body, 2)
	assertIDs(t, got, []string{"4", "5"})
}

func TestSSEGapFill(t *testing.T) {
	env := dbtest.New(t)
	pub, _ := newTestPair(t, env)
	id := resetEvents(t, env, queueRun(t, pub, "sse-gap"))
	ev := events.New(env.App, "redis://127.0.0.1:7341/0")
	appendN(t, ev, id, 3)
	body, cancel := openSSE(t, pub, "/v1/runs/"+id+"/events/stream", "")
	defer cancel()
	readSSE(t, body, 3)
	if _, err := env.App.Exec(t.Context(), `
		UPDATE runs SET last_seq = 4 WHERE id = $1`, uuid.MustParse(id)); err != nil {
		t.Fatal(err)
	}
	if _, err := env.App.Exec(t.Context(), `
		INSERT INTO run_events (run_id, seq, type, attempt, payload)
		VALUES ($1, 4, 'message.assistant', 0, '{}')`, uuid.MustParse(id)); err != nil {
		t.Fatal(err)
	}
	appendN(t, ev, id, 1)
	got := readSSE(t, body, 2)
	assertIDs(t, got, []string{"4", "5"})
}

func TestSSEClosesOnTerminal(t *testing.T) {
	env := dbtest.New(t)
	pub, _ := newTestPair(t, env)
	id := resetEvents(t, env, queueRun(t, pub, "sse-end"))
	ev := events.New(env.App, "redis://127.0.0.1:7341/0")
	body, cancel := openSSE(t, pub, "/v1/runs/"+id+"/events/stream", "")
	defer cancel()
	if _, _, err := ev.Append(t.Context(), uuid.MustParse(id), []events.Event{{
		Type: "run.succeeded", Attempt: 0, Payload: []byte(`{"result":null}`),
	}}, nil); err != nil {
		t.Fatal(err)
	}
	got := readSSE(t, body, 1)
	if got[0].event != "run.succeeded" {
		t.Fatalf("%+v", got)
	}
	select {
	case _, ok := <-body.done:
		if ok {
			t.Fatal("still open")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream stayed open")
	}
}

func TestSSEWorksWithoutRedis(t *testing.T) {
	env := dbtest.New(t)
	app := New(env.App, Options{
		APIKey: "test-key", WorkspacesDir: t.TempDir(), InternalToken: "internal-test",
		LeaseSeconds: 30, HeartbeatSeconds: 10, ClaimMaxWait: 20 * time.Second,
		RedisURL: "redis://127.0.0.1:1/0",
	})
	pub := httptest.NewServer(app.Public)
	t.Cleanup(pub.Close)
	id := resetEvents(t, env, queueRun(t, pub, "sse-noredis"))
	body, cancel := openSSE(t, pub, "/v1/runs/"+id+"/events/stream", "")
	defer cancel()
	ev := events.New(env.App, "")
	appendN(t, ev, id, 1)
	got := readSSE(t, body, 1)
	assertIDs(t, got, []string{"1"})
}

type sseBody struct {
	events chan sseEvent
	done   chan struct{}
}

type sseEvent struct {
	id, event string
}

func openSSE(t *testing.T, pub *httptest.Server, path, lastID string) (*sseBody, func()) {
	t.Helper()
	return openSSEURL(t, pub.URL, path, lastID)
}

func openSSEURL(t *testing.T, base, path, lastID string) (*sseBody, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-key")
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	out := &sseBody{events: make(chan sseEvent, 16), done: make(chan struct{})}
	ready := make(chan struct{})
	go func() {
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			close(ready)
			close(out.done)
			return
		}
		close(ready)
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		var id, ev string
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				if id != "" {
					out.events <- sseEvent{id: id, event: ev}
				}
				id, ev = "", ""
				continue
			}
			if strings.HasPrefix(line, ":") {
				continue
			}
			if strings.HasPrefix(line, "id:") {
				id = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			}
			if strings.HasPrefix(line, "event:") {
				ev = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			}
		}
		close(out.done)
	}()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("sse headers")
	}
	return out, cancel
}

func readSSE(t *testing.T, body *sseBody, n int) []sseEvent {
	t.Helper()
	got := make([]sseEvent, 0, n)
	deadline := time.After(5 * time.Second)
	for len(got) < n {
		select {
		case ev := <-body.events:
			got = append(got, ev)
		case <-deadline:
			t.Fatalf("got %d want %d %+v", len(got), n, got)
		}
	}
	return got
}

func assertIDs(t *testing.T, got []sseEvent, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len %d %+v", len(got), got)
	}
	for i := range want {
		if got[i].id != want[i] {
			t.Fatalf("id %s want %s", got[i].id, want[i])
		}
	}
}

func resetEvents(t *testing.T, env *dbtest.Env, id string) string {
	t.Helper()
	if _, err := env.App.Exec(t.Context(), `DELETE FROM run_events WHERE run_id = $1`, uuid.MustParse(id)); err != nil {
		t.Fatal(err)
	}
	if _, err := env.App.Exec(t.Context(), `UPDATE runs SET last_seq = 0 WHERE id = $1`, uuid.MustParse(id)); err != nil {
		t.Fatal(err)
	}
	return id
}

func appendN(t *testing.T, ev *events.Service, id string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, _, err := ev.Append(t.Context(), uuid.MustParse(id), []events.Event{{
			Type: "message.assistant", Attempt: 0, Payload: []byte(`{"n":1}`),
		}}, nil); err != nil {
			t.Fatal(err)
		}
	}
}
