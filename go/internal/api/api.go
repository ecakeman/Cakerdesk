package api

import (
	"crypto/hmac"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ecakeman/cakerdesk/internal/agents"
	"github.com/ecakeman/cakerdesk/internal/apperr"
	"github.com/ecakeman/cakerdesk/internal/httpx"
	"github.com/ecakeman/cakerdesk/internal/runs"
	"github.com/ecakeman/cakerdesk/internal/sessions"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	Public   *gin.Engine
	Internal *gin.Engine
	pool     *pgxpool.Pool
	agents   *agents.Service
	sessions *sessions.Service
	runs     *runs.Service
	apiKey   string
	internal string
}

type Options struct {
	APIKey           string
	WorkspacesDir    string
	InternalToken    string
	LeaseSeconds     int
	HeartbeatSeconds int
	ClaimMaxWait     time.Duration
}

func New(pool *pgxpool.Pool, opt Options) *Server {
	s := &Server{
		pool:     pool,
		agents:   agents.NewService(pool),
		sessions: sessions.NewService(pool, opt.WorkspacesDir),
		runs:     runs.NewService(pool, opt.LeaseSeconds, opt.HeartbeatSeconds, opt.ClaimMaxWait),
		apiKey:   opt.APIKey,
		internal: opt.InternalToken,
	}
	gin.SetMode(gin.ReleaseMode)
	pub := gin.New()
	pub.Use(httpx.RequestLog(), gin.Recovery())
	pub.GET("/healthz", s.healthz)
	v1 := pub.Group("/v1", s.requireKey)
	v1.POST("/agents", s.createAgent)
	v1.GET("/agents", s.listAgents)
	v1.GET("/agents/:id", s.getAgent)
	v1.POST("/agents/:id/versions", s.publishVersion)
	v1.GET("/agents/:id/versions/:version", s.getVersion)
	v1.POST("/sessions", s.createSession)
	v1.GET("/sessions/:id", s.getSession)
	v1.POST("/sessions/:id/runs", s.createRun)
	v1.GET("/sessions/:id/runs", s.listRuns)
	v1.GET("/runs/:id", s.getRun)
	v1.GET("/runs/:id/config", s.getRunConfig)
	internal := gin.New()
	internal.Use(httpx.RequestLog(), gin.Recovery())
	in := internal.Group("/internal", s.requireInternal)
	in.POST("/runs/claim", s.claimRun)
	in.POST("/runs/:id/complete", s.completeRun)
	s.Public = pub
	s.Internal = internal
	return s
}

// 进程还活着不代表能写数据。库 ping 失败返回 503，避免健康检查把挂掉的库当成正常。
func (s *Server) healthz(c *gin.Context) {
	if err := s.pool.Ping(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (s *Server) requireKey(c *gin.Context) {
	if !bearerOK(c.GetHeader("Authorization"), s.apiKey) {
		httpx.WriteError(c, apperr.New(http.StatusUnauthorized, "unauthorized", "未授权"))
	}
}

func (s *Server) requireInternal(c *gin.Context) {
	// 公共 API Key 不能领 Run。内部端口只认这一把钥匙。
	if !bearerOK(c.GetHeader("Authorization"), s.internal) {
		httpx.WriteError(c, apperr.New(http.StatusUnauthorized, "unauthorized", "未授权"))
	}
}

func bearerOK(header, key string) bool {
	// 没有 "Bearer " 前缀时 TrimPrefix 原样返回，下面当成没带钥匙。
	// hmac.Equal 不因长度提前返回，避免用耗时猜钥匙。
	got := strings.TrimPrefix(header, "Bearer ")
	if got == header || key == "" {
		return false
	}
	return hmac.Equal([]byte(got), []byte(key))
}

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

func (s *Server) getAgent(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		// 格式不对也 404。客户端不用区分「写错了」和「没有这行」。
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
	// 200：配置没变，不能再插一行。201：新版本。码由 Publish 决定。
	c.JSON(status, versionJSON(ver))
}

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

func (s *Server) claimRun(c *gin.Context) {
	var req struct {
		WorkerID string `json:"worker_id"`
		WaitMS   int    `json:"wait_ms"`
	}
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.WriteError(c, err)
		return
	}
	claim, ok, err := s.runs.Claim(c.Request.Context(), req.WorkerID, req.WaitMS)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	if !ok {
		c.Status(http.StatusNoContent)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"run_id":            claim.RunID.String(),
		"session_id":        claim.SessionID.String(),
		"attempt":           claim.Attempt,
		"input":             claim.Input,
		"config":            claim.Config,
		"tools":             claim.Tools,
		"lease_seconds":     claim.LeaseSeconds,
		"heartbeat_seconds": claim.HeartbeatSeconds,
	})
}

func (s *Server) completeRun(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.WriteError(c, apperr.New(http.StatusNotFound, "not_found", "run 不存在"))
		return
	}
	var req struct {
		WorkerID string          `json:"worker_id"`
		Attempt  int32           `json:"attempt"`
		Status   string          `json:"status"`
		Result   json.RawMessage `json:"result"`
		Error    *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.WriteError(c, err)
		return
	}
	in := runs.CompleteInput{
		RunID:    id,
		WorkerID: req.WorkerID,
		Attempt:  req.Attempt,
		Status:   req.Status,
		Result:   req.Result,
	}
	if req.Error != nil {
		in.Error = &struct {
			Code    string
			Message string
		}{Code: req.Error.Code, Message: req.Error.Message}
	}
	if err := s.runs.Complete(c.Request.Context(), in); err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func agentJSON(a agents.Agent) gin.H {
	return gin.H{
		"id":              a.ID.String(),
		"name":            a.Name,
		"current_version": a.CurrentVersion,
	}
}

func (s *Server) createSession(c *gin.Context) {
	var req struct {
		AgentID string `json:"agent_id"`
		Title   string `json:"title"`
	}
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.WriteError(c, err)
		return
	}
	id, err := uuid.Parse(req.AgentID)
	if err != nil {
		httpx.WriteError(c, apperr.New(http.StatusNotFound, "not_found", "agent 不存在"))
		return
	}
	sess, err := s.sessions.Create(c.Request.Context(), id, req.Title)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(http.StatusCreated, sessionJSON(sess))
}

func (s *Server) getSession(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.WriteError(c, apperr.New(http.StatusNotFound, "not_found", "session 不存在"))
		return
	}
	sess, err := s.sessions.Get(c.Request.Context(), id)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, sessionJSON(sess))
}

func (s *Server) createRun(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.WriteError(c, apperr.New(http.StatusNotFound, "not_found", "session 不存在"))
		return
	}
	var req struct {
		Input string `json:"input"`
	}
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.WriteError(c, err)
		return
	}
	run, err := s.sessions.CreateRun(c.Request.Context(), id, req.Input)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(http.StatusCreated, runJSON(run))
}

func (s *Server) getRun(c *gin.Context) {
	run, err := s.loadRun(c)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, runJSON(run))
}

func (s *Server) getRunConfig(c *gin.Context) {
	run, err := s.loadRun(c)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"config": run.Config})
}

func (s *Server) listRuns(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.WriteError(c, apperr.New(http.StatusNotFound, "not_found", "session 不存在"))
		return
	}
	items, err := s.sessions.ListRuns(c.Request.Context(), id)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	out := make([]gin.H, 0, len(items))
	for _, run := range items {
		out = append(out, runJSON(run))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (s *Server) loadRun(c *gin.Context) (sessions.Run, error) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return sessions.Run{}, apperr.New(http.StatusNotFound, "not_found", "run 不存在")
	}
	return s.sessions.GetRun(c.Request.Context(), id)
}

func sessionJSON(sess sessions.Session) gin.H {
	var active any
	if sess.ActiveRunID != nil {
		active = sess.ActiveRunID.String()
	}
	return gin.H{
		"id":            sess.ID.String(),
		"agent_id":      sess.AgentID.String(),
		"title":         sess.Title,
		"created_at":    sess.CreatedAt,
		"active_run_id": active,
	}
}

func runJSON(run sessions.Run) gin.H {
	var errBody any
	if run.ErrorCode != nil || run.ErrorMessage != nil {
		code, msg := "", ""
		if run.ErrorCode != nil {
			code = *run.ErrorCode
		}
		if run.ErrorMessage != nil {
			msg = *run.ErrorMessage
		}
		errBody = gin.H{"code": code, "message": msg}
	}
	return gin.H{
		"id":            run.ID.String(),
		"session_id":    run.SessionID.String(),
		"agent_version": run.AgentVersion,
		"status":        run.Status,
		"attempt":       run.Attempt,
		"result":        run.Result,
		"error":         errBody,
		"created_at":    run.CreatedAt,
		"started_at":    run.StartedAt,
		"finished_at":   run.FinishedAt,
	}
}

func versionJSON(v agents.Version) gin.H {
	return gin.H{
		"version":     v.Version,
		"config":      v.Config,
		"config_hash": v.Hash,
	}
}
