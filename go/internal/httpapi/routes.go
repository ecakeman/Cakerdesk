package httpapi

import (
	"net/http"
	"strings"

	authsvc "cakerdesk/internal/auth/service"
	"cakerdesk/internal/platform/apperr"
	"cakerdesk/internal/platform/crypto"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func (s *Server) mount() {
	s.Public.GET("/healthz", s.requirePerm(authsvc.PermPublic), s.healthz)
	s.Public.GET("/readyz", s.requirePerm(authsvc.PermPublic), s.readyz)
	s.Public.POST("/v1/auth/login", s.requirePerm(authsvc.PermPublic), s.login)
	s.Public.POST("/v1/auth/logout", s.requirePerm(authsvc.PermAgentRead), s.logout)
	s.Public.GET("/v1/api-keys", s.requirePerm(authsvc.PermMemberWrite), s.listKeys)
	s.Public.POST("/v1/api-keys", s.requirePerm(authsvc.PermMemberWrite), s.idempotent, s.createKey)
	s.Public.DELETE("/v1/api-keys/:id", s.requirePerm(authsvc.PermMemberWrite), s.revokeKey)
	s.Public.GET("/v1/agents", s.requirePerm(authsvc.PermAgentRead), s.listAgents)
	s.Public.POST("/v1/agents", s.requirePerm(authsvc.PermAgentWrite), s.idempotent, s.createAgent)
	s.Public.GET("/v1/agents/:id", s.requirePerm(authsvc.PermAgentRead), s.getAgent)
	s.Public.PATCH("/v1/agents/:id", s.requirePerm(authsvc.PermAgentWrite), s.patchAgent)
	s.Public.DELETE("/v1/agents/:id", s.requirePerm(authsvc.PermAgentWrite), s.archiveAgent)
	s.Public.GET("/v1/agents/:id/versions", s.requirePerm(authsvc.PermAgentRead), s.listVersions)
	s.Public.POST("/v1/agents/:id/versions", s.requirePerm(authsvc.PermAgentWrite), s.idempotent, s.publishVersion)
	s.Public.GET("/v1/agents/:id/versions/diff", s.requirePerm(authsvc.PermAgentRead), s.diffVersions)
	s.Public.GET("/v1/agents/:id/versions/:v", s.requirePerm(authsvc.PermAgentRead), s.getVersion)
	s.Public.POST("/v1/agents/:id/current-version", s.requirePerm(authsvc.PermAgentWrite), s.setCurrent)
	s.Internal.GET("/metrics", s.requirePerm(authsvc.PermPublic), gin.WrapH(promhttp.Handler()))
	s.Internal.GET("/readyz", s.requirePerm(authsvc.PermPublic), s.readyz)
}

func (s *Server) requirePerm(perm string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if perm != authsvc.PermPublic {
			actor, err := s.authenticate(c)
			if err != nil {
				writeErr(c, err)
				c.Abort()
				return
			}
			if !authsvc.Allowed(actor.Role, perm) {
				writeErr(c, apperr.Forbidden())
				c.Abort()
				return
			}
			c.Set("actor", actor)
		}
		c.Next()
	}
}

func (s *Server) authenticate(c *gin.Context) (authsvc.Actor, error) {
	if h := c.GetHeader("Authorization"); h != "" {
		raw, ok := strings.CutPrefix(h, "Bearer ")
		if !ok || raw == "" {
			return authsvc.Actor{}, apperr.Unauth("缺少凭证")
		}
		return s.auth.ActorFromAPIKey(c.Request.Context(), raw)
	}
	cookie, err := c.Cookie("cd_session")
	if err != nil || cookie == "" {
		return authsvc.Actor{}, apperr.Unauth("缺少凭证")
	}
	return s.auth.ActorFromSession(c.Request.Context(), cookie)
}

func (s *Server) healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (s *Server) readyz(c *gin.Context) {
	if err := s.pool.Ping(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "service.unavailable", "message": "database", "details": []any{}}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}

func (s *Server) idempotent(c *gin.Context) {
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if key == "" {
		c.Next()
		return
	}
	body, err := readBody(c)
	if err != nil {
		writeErr(c, apperr.JSON())
		c.Abort()
		return
	}
	actor := actorOf(c)
	sum := crypto.SHA256(body)
	status, stored, hit, err := s.auth.ReadIdempotency(c.Request.Context(), actor.TenantID, key, sum)
	if err != nil {
		writeErr(c, err)
		c.Abort()
		return
	}
	if hit {
		c.Data(status, "application/json", stored)
		c.Abort()
		return
	}
	cap := &capture{ResponseWriter: c.Writer}
	c.Writer = cap
	c.Next()
	code := cap.code
	if code == 0 {
		code = http.StatusOK
	}
	if cap.wrote && code >= 200 && code < 300 {
		_ = s.auth.SaveIdempotency(c.Request.Context(), actor.TenantID, key, sum, code, cap.buf.Bytes())
	}
}
