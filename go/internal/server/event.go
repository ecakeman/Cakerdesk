package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"cakerdesk/internal/db"
)

func now() pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
}

func (s *Server) ingest(c *gin.Context) {
	var body struct {
		RunID     string          `json:"run_id"`
		Seq       int64           `json:"seq"`
		Type      string          `json:"type"`
		Timestamp string          `json:"timestamp"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad event"})
		return
	}
	if body.RunID == "" {
		body.RunID = c.Param("runID")
	}
	if body.RunID != c.Param("runID") || body.Seq <= 0 || body.Type == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad event"})
		return
	}
	ctx := c.Request.Context()
	if _, err := s.Q.GetRun(ctx, body.RunID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	inserted, err := s.Q.InsertEvent(ctx, db.InsertEventParams{
		RunID: body.RunID, Seq: body.Seq, Type: body.Type, Timestamp: body.Timestamp, Payload: string(body.Payload),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	event := db.Event{RunID: body.RunID, Seq: body.Seq, Type: body.Type, Timestamp: body.Timestamp, Payload: string(body.Payload)}
	if inserted > 0 {
		if err := s.applySnapshot(c, event); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		s.publish(event)
		s.projectMessage(c, event)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "inserted": inserted > 0})
}

func (s *Server) applySnapshot(c *gin.Context, event db.Event) error {
	ctx := c.Request.Context()
	switch event.Type {
	case "plan.updated":
		return s.Q.UpdatePlanSnapshot(ctx, db.UpdatePlanSnapshotParams{ID: event.RunID, PlanSnapshot: event.Payload})
	case "verification.completed":
		return s.Q.UpdateVerificationSnapshot(ctx, db.UpdateVerificationSnapshotParams{ID: event.RunID, VerificationSnapshot: event.Payload})
	case "run.completed":
		return s.Q.CompleteRun(ctx, db.CompleteRunParams{ID: event.RunID, ArtifactPaths: artifactPaths(event.Payload)})
	case "run.failed":
		return s.Q.SetRunStatus(ctx, db.SetRunStatusParams{ID: event.RunID, Status: "failed"})
	case "run.cancelled":
		return s.Q.SetRunStatus(ctx, db.SetRunStatusParams{ID: event.RunID, Status: "cancelled"})
	default:
		return nil
	}
}

func artifactPaths(payload string) string {
	var body struct {
		Artifacts []string `json:"artifacts"`
	}
	if err := json.Unmarshal([]byte(payload), &body); err != nil || body.Artifacts == nil {
		return "[]"
	}
	raw, _ := json.Marshal(body.Artifacts)
	return string(raw)
}

func (s *Server) projectMessage(c *gin.Context, event db.Event) {
	run, err := s.Q.GetRun(c.Request.Context(), event.RunID)
	if err != nil {
		return
	}
	switch event.Type {
	case "model.message":
		var payload struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal([]byte(event.Payload), &payload)
		if payload.Content != "" {
			_ = s.Q.AddMessage(c.Request.Context(), db.AddMessageParams{
				ID: uuid.NewString(), ThreadID: run.ThreadID, Role: "assistant", Content: payload.Content, CreatedAt: now(),
			})
		}
	case "tool.completed":
		var payload struct {
			Tool    string `json:"tool"`
			Status  string `json:"status"`
			Summary string `json:"summary"`
		}
		_ = json.Unmarshal([]byte(event.Payload), &payload)
		_ = s.Q.AddMessage(c.Request.Context(), db.AddMessageParams{
			ID: uuid.NewString(), ThreadID: run.ThreadID, Role: "tool", Content: payload.Tool + " " + payload.Status + " " + payload.Summary, CreatedAt: now(),
		})
	}
}

func (s *Server) streamEvents(c *gin.Context) {
	after := int64(0)
	if raw := c.Query("after_seq"); raw != "" {
		after, _ = strconv.ParseInt(raw, 10, 64)
	} else if raw := c.GetHeader("Last-Event-ID"); raw != "" {
		after, _ = strconv.ParseInt(raw, 10, 64)
	}
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	runID := c.Param("runID")
	existing, err := s.Q.ListEventsAfter(c.Request.Context(), db.ListEventsAfterParams{RunID: runID, Seq: after})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, event := range existing {
		writeSSE(c, event)
		after = event.Seq
	}
	c.Writer.Flush()
	ch := make(chan db.Event, 16)
	s.addSub(runID, ch)
	defer s.removeSub(runID, ch)
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case event := <-ch:
			if event.Seq <= after {
				continue
			}
			writeSSE(c, event)
			after = event.Seq
			c.Writer.Flush()
		}
	}
}

func writeSSE(c *gin.Context, event db.Event) {
	body, _ := json.Marshal(map[string]any{
		"run_id": event.RunID, "seq": event.Seq, "type": event.Type, "timestamp": event.Timestamp, "payload": json.RawMessage(event.Payload),
	})
	fmt.Fprintf(c.Writer, "id: %d\nevent: %s\ndata: %s\n\n", event.Seq, event.Type, body)
}
