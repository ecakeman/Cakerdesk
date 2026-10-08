package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"cakerdesk/internal/db"
)

func (s *Server) createProject(c *gin.Context) {
	var body struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name required"})
		return
	}
	id := uuid.NewString()
	err := s.Q.CreateProject(c.Request.Context(), db.CreateProjectParams{ID: id, Name: body.Name, CreatedAt: now()})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id, "name": body.Name})
}

func (s *Server) listProjects(c *gin.Context) {
	items, err := s.Q.ListProjects(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, projectsJSON(items))
}

func (s *Server) listMemory(c *gin.Context) {
	raw, err := s.Python.ListMemory(c.Request.Context(), c.Param("projectID"))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.Data(http.StatusOK, "application/json", raw)
}
