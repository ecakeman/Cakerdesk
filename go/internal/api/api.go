package api

import (
	"crypto/hmac"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/ecakeman/cakerdesk/internal/agents"
	"github.com/ecakeman/cakerdesk/internal/apperr"
	"github.com/ecakeman/cakerdesk/internal/httpx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	Public   *gin.Engine
	Internal *gin.Engine
	pool     *pgxpool.Pool
	agents   *agents.Service
	apiKey   string
}

// New 装配两个 Gin 引擎并挂上当前步骤的路由。
func New(pool *pgxpool.Pool, apiKey string) *Server {
	s := &Server{
		pool:   pool,
		agents: agents.NewService(pool),
		apiKey: apiKey,
	}
	gin.SetMode(gin.ReleaseMode)
	// Public：健康检查 + 带钥匙的 /v1。Internal 这步只建引擎，不监听。
	pub := gin.New()
	pub.Use(httpx.RequestLog(), gin.Recovery())
	pub.GET("/healthz", s.healthz)
	v1 := pub.Group("/v1", s.requireKey)
	v1.POST("/agents", s.createAgent)
	v1.GET("/agents", s.listAgents)
	v1.GET("/agents/:id", s.getAgent)
	v1.POST("/agents/:id/versions", s.publishVersion)
	v1.GET("/agents/:id/versions/:version", s.getVersion)
	internal := gin.New()
	internal.Use(gin.Recovery())
	s.Public = pub
	s.Internal = internal
	return s
}

// healthz 探活数据库；连不上对监控来说就是挂了。
func (s *Server) healthz(c *gin.Context) {
	if err := s.pool.Ping(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// requireKey 校验公共 API 的静态钥匙。缺 Bearer 或对不上都 401。
func (s *Server) requireKey(c *gin.Context) {
	got := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	if got == c.GetHeader("Authorization") || !hmac.Equal([]byte(got), []byte(s.apiKey)) {
		httpx.WriteError(c, apperr.New(http.StatusUnauthorized, "unauthorized", "未授权"))
		return
	}
}

// createAgent POST /v1/agents
func (s *Server) createAgent(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
	}
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.WriteError(c, err)
		return
	}
	agent, err := s.agents.Create(c.Request.Context(), req.Name)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(http.StatusCreated, agentJSON(agent))
}

// listAgents GET /v1/agents，最多 200 条。
func (s *Server) listAgents(c *gin.Context) {
	items, err := s.agents.List(c.Request.Context())
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	out := make([]gin.H, 0, len(items))
	for _, a := range items {
		out = append(out, agentJSON(a))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// getAgent GET /v1/agents/:id，非法 uuid 与查无都当 404。
func (s *Server) getAgent(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.WriteError(c, apperr.New(http.StatusNotFound, "not_found", "agent 不存在"))
		return
	}
	agent, err := s.agents.Get(c.Request.Context(), id)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, agentJSON(agent))
}

// publishVersion POST /v1/agents/:id/versions。HTTP 码由 Publish 决定（200 幂等 / 201 新版本）。
func (s *Server) publishVersion(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.WriteError(c, apperr.New(http.StatusNotFound, "not_found", "agent 不存在"))
		return
	}
	var req struct {
		Config json.RawMessage `json:"config"`
	}
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.WriteError(c, err)
		return
	}
	ver, status, err := s.agents.Publish(c.Request.Context(), id, req.Config)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(status, versionJSON(ver))
}

// getVersion GET /v1/agents/:id/versions/:version
func (s *Server) getVersion(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.WriteError(c, apperr.New(http.StatusNotFound, "not_found", "agent 不存在"))
		return
	}
	n, err := strconv.Atoi(c.Param("version"))
	if err != nil {
		httpx.WriteError(c, apperr.New(http.StatusNotFound, "not_found", "version 不存在"))
		return
	}
	ver, err := s.agents.GetVersion(c.Request.Context(), id, int32(n))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, versionJSON(ver))
}

// agentJSON 列表和详情共用的对外字段。
func agentJSON(a agents.Agent) gin.H {
	return gin.H{
		"id":              a.ID.String(),
		"name":            a.Name,
		"current_version": a.CurrentVersion,
	}
}

// versionJSON 返回规范化后的 config 和 hash。
func versionJSON(v agents.Version) gin.H {
	return gin.H{
		"version":     v.Version,
		"config":      v.Config,
		"config_hash": v.Hash,
	}
}
