package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	agentsvc "cakerdesk/internal/agents/service"
	authsvc "cakerdesk/internal/auth/service"
	"cakerdesk/internal/platform/apperr"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	pool         *pgxpool.Pool
	auth         *authsvc.Service
	agents       *agentsvc.Service
	secureCookie bool
	Public       *gin.Engine
	Internal     *gin.Engine
}

func New(pool *pgxpool.Pool, auth *authsvc.Service, agents *agentsvc.Service, secureCookie bool) *Server {
	gin.SetMode(gin.ReleaseMode)
	s := &Server{
		pool:         pool,
		auth:         auth,
		agents:       agents,
		secureCookie: secureCookie,
		Public:       gin.New(),
		Internal:     gin.New(),
	}
	s.Public.Use(gin.Recovery(), s.log)
	s.Internal.Use(gin.Recovery())
	s.mount()
	return s
}

func (s *Server) log(c *gin.Context) {
	start := time.Now()
	c.Next()
	slog.Info("http", "method", c.Request.Method, "path", c.Request.URL.Path, "status", c.Writer.Status(), "dur_ms", time.Since(start).Milliseconds())
}

func writeErr(c *gin.Context, err error) {
	ae, ok := apperr.As(err)
	if !ok || ae == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "service.unavailable", "message": "内部错误", "details": []any{}}})
		return
	}
	details := ae.Details
	if details == nil {
		details = []apperr.Detail{}
	}
	c.JSON(ae.Status, gin.H{"error": gin.H{"code": ae.Code, "message": ae.Message, "details": details}})
}

func actorOf(c *gin.Context) authsvc.Actor {
	return c.MustGet("actor").(authsvc.Actor)
}
