package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWatchStopsOnCompleted(t *testing.T) {
	assertWatchReturns(t, "run.completed", `{"summary":"任务完成","artifacts":["artifacts/report.md"]}`, "completed")
}

func TestWatchStopsOnFailed(t *testing.T) {
	assertWatchReturns(t, "run.failed", `{"reason":"model_error","message":"provider request failed"}`, "failed")
}

func TestWatchStopsOnCancelled(t *testing.T) {
	assertWatchReturns(t, "run.cancelled", `{"reason":"user_cancelled"}`, "cancelled")
}

func TestWatchStopsOnLiveTerminalEvent(t *testing.T) {
	hold := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, sseLine(1, "run.started", `{"goal":"写报告"}`))
		flusher.Flush()
		time.Sleep(30 * time.Millisecond)
		fmt.Fprint(w, sseLine(2, "run.failed", `{"reason":"model_error","message":"provider request failed"}`))
		flusher.Flush()
		<-hold
	}))
	defer server.Close()
	defer close(hold)
	var out strings.Builder
	status, err := followRun(t.Context(), Options{BaseURL: server.URL, Out: &out}, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("status %s output %s", status, out.String())
	}
	if !strings.Contains(out.String(), "run.started 写报告") || !strings.Contains(out.String(), "run.failed model_error") {
		t.Fatalf("%s", out.String())
	}
}

func assertWatchReturns(t *testing.T, eventType, payload, want string) {
	t.Helper()
	hold := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, sseLine(1, "run.started", `{"goal":"写报告"}`))
		fmt.Fprint(w, sseLine(2, eventType, payload))
		flusher.Flush()
		<-hold
	}))
	defer server.Close()
	defer close(hold)
	done := make(chan string, 1)
	go func() {
		status, err := followRun(t.Context(), Options{BaseURL: server.URL, Out: &strings.Builder{}}, "run-1")
		if err != nil {
			done <- err.Error()
			return
		}
		done <- status
	}()
	select {
	case status := <-done:
		if status != want {
			t.Fatalf("status %s", status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not return")
	}
}

func sseLine(seq int, eventType, payload string) string {
	return fmt.Sprintf("id: %d\nevent: %s\ndata: {\"type\":%q,\"payload\":%s}\n\n", seq, eventType, eventType, payload)
}
