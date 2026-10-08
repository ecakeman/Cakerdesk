package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"cakerdesk/internal/db"
	"cakerdesk/internal/pythonclient"
	"cakerdesk/internal/workspace"
)

func (s *Server) createRun(c *gin.Context) {
	var body struct {
		Goal string `json:"goal"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Goal == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "goal required"})
		return
	}
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project_id required"})
		return
	}
	threadID := c.Param("threadID")
	runID := uuid.NewString()
	ctx := c.Request.Context()
	err := s.Q.CreateRun(ctx, db.CreateRunParams{
		ID:        runID,
		ProjectID: projectID,
		ThreadID:  threadID,
		Goal:      body.Goal,
		Status:    "running",
		CreatedAt: now(),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	_ = s.Q.AddMessage(ctx, db.AddMessageParams{
		ID: uuid.NewString(), ThreadID: threadID, Role: "user", Content: body.Goal, CreatedAt: now(),
	})
	dir, err := workspace.EnsureThread(s.Workspace, threadID)
	if err != nil {
		_ = s.Q.SetRunStatus(ctx, db.SetRunStatusParams{ID: runID, Status: "failed"})
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	err = s.Python.Start(ctx, pythonclient.RunBody{
		ProjectID: projectID, ThreadID: threadID, RunID: runID, Goal: body.Goal, WorkspaceRoot: dir,
	})
	if err != nil {
		_ = s.Q.SetRunStatus(ctx, db.SetRunStatusParams{ID: runID, Status: "failed"})
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	run, err := s.Q.GetRun(ctx, runID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, runJSON(run))
}

func (s *Server) getRun(c *gin.Context) {
	run, err := s.Q.GetRun(c.Request.Context(), c.Param("runID"))
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, runJSON(run))
}

func (s *Server) cancelRun(c *gin.Context) {
	runID := c.Param("runID")
	ctx := c.Request.Context()
	if _, err := s.Q.GetRun(ctx, runID); errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if _, err := s.Q.RequestCancel(ctx, runID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	_ = s.Python.Cancel(ctx, runID)
	run, _ := s.Q.GetRun(ctx, runID)
	c.JSON(http.StatusAccepted, runJSON(run))
}

func (s *Server) resumeRun(c *gin.Context) {
	ctx := c.Request.Context()
	run, err := s.Q.GetRun(ctx, c.Param("runID"))
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if run.Status == "completed" || run.Status == "cancelled" {
		c.JSON(http.StatusConflict, gin.H{"error": "run already finished"})
		return
	}
	dir, err := workspace.EnsureThread(s.Workspace, run.ThreadID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	err = s.Python.Resume(ctx, pythonclient.RunBody{
		ProjectID: run.ProjectID, ThreadID: run.ThreadID, RunID: run.ID, Goal: run.Goal, WorkspaceRoot: dir,
	})
	if err != nil {
		if strings.Contains(err.Error(), "409") {
			c.JSON(http.StatusConflict, gin.H{"error": "No resumable checkpoint."})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if run.Status == "failed" {
		_ = s.Q.SetRunStatus(ctx, db.SetRunStatusParams{ID: run.ID, Status: "running"})
		run.Status = "running"
	}
	c.JSON(http.StatusAccepted, runJSON(run))
}

func (s *Server) listArtifacts(c *gin.Context) {
	run, err := s.Q.GetRun(c.Request.Context(), c.Param("runID"))
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var paths []string
	_ = json.Unmarshal([]byte(run.ArtifactPaths), &paths)
	if paths == nil {
		paths = []string{}
	}
	root := filepath.Join(s.Workspace, "threads", run.ThreadID)
	out := make([]gin.H, 0, len(paths))
	for _, rel := range paths {
		_, statErr := os.Stat(filepath.Join(root, rel))
		out = append(out, gin.H{"path": rel, "exists": statErr == nil})
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) readFile(c *gin.Context) {
	run, err := s.Q.GetRun(c.Request.Context(), c.Param("runID"))
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	rel := strings.TrimPrefix(c.Param("path"), "/")
	root := filepath.Join(s.Workspace, "threads", run.ThreadID)
	full, err := workspace.Resolve(root, rel)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid path"})
		return
	}
	body, err := os.ReadFile(full)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", body)
}
