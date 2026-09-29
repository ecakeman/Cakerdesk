package api

import (
	"net/http"

	"github.com/ecakeman/cakerdesk/internal/httpx"
	"github.com/gin-gonic/gin"
)

type Server struct {
	Public   *gin.Engine
	Internal *gin.Engine
}

func New() *Server {
	gin.SetMode(gin.ReleaseMode)
	pub := gin.New()
	pub.Use(httpx.RequestLog(), gin.Recovery())
	pub.GET("/healthz", healthz)
	internal := gin.New()
	internal.Use(gin.Recovery())
	return &Server{Public: pub, Internal: internal}
}

func healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
