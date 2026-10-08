package server

import (
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"cakerdesk/internal/db"
	"cakerdesk/internal/pythonclient"
)

type Server struct {
	Pool      *pgxpool.Pool
	Q         *db.Queries
	Python    pythonclient.Client
	Workspace string

	mu   sync.Mutex
	subs map[string]map[chan db.Event]struct{}
}

func (s *Server) Router() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	router.POST("/api/projects", s.createProject)
	router.GET("/api/projects", s.listProjects)
	router.GET("/api/projects/:projectID/memory", s.listMemory)
	router.POST("/api/projects/:projectID/threads", s.createThread)
	router.GET("/api/projects/:projectID/threads", s.listThreads)
	router.POST("/api/threads/:threadID/runs", s.createRun)
	router.GET("/api/runs/:runID", s.getRun)
	router.POST("/api/runs/:runID/cancel", s.cancelRun)
	router.POST("/api/runs/:runID/resume", s.resumeRun)
	router.GET("/api/runs/:runID/events", s.streamEvents)
	router.GET("/api/runs/:runID/artifacts", s.listArtifacts)
	router.GET("/api/threads/:threadID/messages", s.listMessages)
	router.GET("/internal/runs/:runID", s.getRun)
	router.POST("/internal/runs/:runID/events", s.ingest)
	return router
}

func (s *Server) publish(event db.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs[event.RunID] {
		select {
		case ch <- event:
		default:
		}
	}
}

func (s *Server) addSub(runID string, ch chan db.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subs == nil {
		s.subs = map[string]map[chan db.Event]struct{}{}
	}
	if s.subs[runID] == nil {
		s.subs[runID] = map[chan db.Event]struct{}{}
	}
	s.subs[runID][ch] = struct{}{}
}

func (s *Server) removeSub(runID string, ch chan db.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subs[runID], ch)
}
