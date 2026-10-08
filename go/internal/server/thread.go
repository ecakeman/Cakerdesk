package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"cakerdesk/internal/db"
	"cakerdesk/internal/workspace"
)

func (s *Server) createThread(c *gin.Context) {
	var body struct {
		Title string `json:"title"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title required"})
		return
	}
	id := uuid.NewString()
	err := s.Q.CreateThread(c.Request.Context(), db.CreateThreadParams{
		ID:        id,
		ProjectID: c.Param("projectID"),
		Title:     body.Title,
		CreatedAt: now(),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if _, err := workspace.EnsureThread(s.Workspace, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id, "title": body.Title})
}

func (s *Server) listThreads(c *gin.Context) {
	items, err := s.Q.ListThreads(c.Request.Context(), c.Param("projectID"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, threadsJSON(items))
}

func (s *Server) listRuns(c *gin.Context) {
	items, err := s.Q.ListRunsByThread(c.Request.Context(), c.Param("threadID"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]gin.H, 0, len(items))
	for _, item := range items {
		out = append(out, gin.H{"id": item.ID, "goal": item.Goal, "status": item.Status})
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) listMessages(c *gin.Context) {
	items, err := s.Q.ListMessages(c.Request.Context(), c.Param("threadID"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, messagesJSON(items))
}
