package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	agentsvc "cakerdesk/internal/agents/service"
	authsvc "cakerdesk/internal/auth/service"
	"cakerdesk/internal/platform/apperr"
	"cakerdesk/internal/platform/ids"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (s *Server) Bootstrap(ctx context.Context, email, tenant, password string) error {
	return s.auth.Bootstrap(ctx, email, tenant, password)
}

func (s *Server) login(c *gin.Context) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Tenant   string `json:"tenant"`
	}
	if err := bind(c, &req); err != nil {
		writeErr(c, err)
		return
	}
	res, err := s.auth.Login(c.Request.Context(), req.Email, req.Tenant, req.Password)
	if err != nil {
		writeErr(c, err)
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name: "cd_session", Value: res.Token, Path: "/", HttpOnly: true,
		Secure: s.secureCookie, SameSite: http.SameSiteLaxMode, MaxAge: 7 * 24 * 3600,
	})
	c.JSON(http.StatusOK, gin.H{
		"user":   gin.H{"id": ids.Format(ids.User, res.UserID), "email": res.Email, "role": res.Role},
		"tenant": gin.H{"id": ids.Format(ids.Tenant, res.TenantID)},
	})
}

func (s *Server) logout(c *gin.Context) {
	actor := actorOf(c)
	if len(actor.Session) > 0 {
		_ = s.auth.Logout(c.Request.Context(), actor.Session)
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: "cd_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secureCookie})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) listKeys(c *gin.Context) {
	items, err := s.auth.ListAPIKeys(c.Request.Context(), actorOf(c))
	if err != nil {
		writeErr(c, err)
		return
	}
	out := make([]gin.H, 0, len(items))
	for _, k := range items {
		out = append(out, keyJSON(k, ""))
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "next_cursor": nil})
}

func (s *Server) createKey(c *gin.Context) {
	var req struct {
		Name      string     `json:"name"`
		Role      string     `json:"role"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := bind(c, &req); err != nil {
		writeErr(c, err)
		return
	}
	created, err := s.auth.CreateAPIKey(c.Request.Context(), actorOf(c), req.Name, req.Role, req.ExpiresAt, c.ClientIP())
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, keyJSON(created.Key, created.Secret))
}

func (s *Server) revokeKey(c *gin.Context) {
	id, err := ids.Parse(ids.APIKey, c.Param("id"))
	if err != nil {
		writeErr(c, apperr.NotFound())
		return
	}
	if err := s.auth.RevokeAPIKey(c.Request.Context(), actorOf(c), id, c.ClientIP()); err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) listAgents(c *gin.Context) {
	limit := 20
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeErr(c, apperr.Invalid("limit 不合法"))
			return
		}
		if n > 100 {
			n = 100
		}
		limit = n
	}
	var cursorAt *time.Time
	var cursorID *uuid.UUID
	if raw := c.Query("cursor"); raw != "" {
		t, id, err := decodeCursor(raw)
		if err != nil {
			writeErr(c, apperr.Invalid("cursor 不合法"))
			return
		}
		cursorAt, cursorID = &t, &id
	}
	items, err := s.agents.List(c.Request.Context(), actorOf(c).TenantID, cursorAt, cursorID, limit+1)
	if err != nil {
		writeErr(c, err)
		return
	}
	var next any
	if len(items) > limit {
		last := items[limit-1]
		next = encodeCursor(last.CreatedAt, last.ID)
		items = items[:limit]
	}
	out := make([]gin.H, 0, len(items))
	for _, a := range items {
		out = append(out, agentJSON(a))
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "next_cursor": next})
}

func (s *Server) createAgent(c *gin.Context) {
	var req struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Config      json.RawMessage `json:"config"`
	}
	if err := bind(c, &req); err != nil {
		writeErr(c, err)
		return
	}
	actor := actorOf(c)
	agent, ver, err := s.agents.Create(c.Request.Context(), actor.TenantID, actor.UserID, req.Name, req.Description, req.Config, c.ClientIP())
	if err != nil {
		writeErr(c, err)
		return
	}
	body := gin.H{"agent": agentJSON(agent)}
	if ver != nil {
		body["version"] = versionJSON(*ver, nil)
	}
	writeAgent(c, http.StatusCreated, agent.Version, body)
}

func (s *Server) getAgent(c *gin.Context) {
	id, err := ids.Parse(ids.Agent, c.Param("id"))
	if err != nil {
		writeErr(c, apperr.NotFound())
		return
	}
	agent, err := s.agents.Get(c.Request.Context(), actorOf(c).TenantID, id)
	if err != nil {
		writeErr(c, err)
		return
	}
	writeAgent(c, http.StatusOK, agent.Version, agentJSON(agent))
}

func (s *Server) patchAgent(c *gin.Context) {
	id, expect, err := s.agentMatch(c)
	if err != nil {
		writeErr(c, err)
		return
	}
	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if err := bind(c, &req); err != nil {
		writeErr(c, err)
		return
	}
	agent, err := s.agents.Update(c.Request.Context(), actorOf(c).TenantID, id, req.Name, req.Description, expect)
	if err != nil {
		writeErr(c, err)
		return
	}
	writeAgent(c, http.StatusOK, agent.Version, agentJSON(agent))
}

func (s *Server) archiveAgent(c *gin.Context) {
	id, expect, err := s.agentMatch(c)
	if err != nil {
		writeErr(c, err)
		return
	}
	agent, err := s.agents.Archive(c.Request.Context(), actorOf(c).TenantID, id, expect)
	if err != nil {
		writeErr(c, err)
		return
	}
	writeAgent(c, http.StatusOK, agent.Version, agentJSON(agent))
}

func (s *Server) listVersions(c *gin.Context) {
	id, err := ids.Parse(ids.Agent, c.Param("id"))
	if err != nil {
		writeErr(c, apperr.NotFound())
		return
	}
	items, err := s.agents.Versions(c.Request.Context(), actorOf(c).TenantID, id)
	if err != nil {
		writeErr(c, err)
		return
	}
	out := make([]gin.H, 0, len(items))
	for _, v := range items {
		out = append(out, versionJSON(v, nil))
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "next_cursor": nil})
}

func (s *Server) publishVersion(c *gin.Context) {
	id, err := ids.Parse(ids.Agent, c.Param("id"))
	if err != nil {
		writeErr(c, apperr.NotFound())
		return
	}
	var req struct {
		Config json.RawMessage `json:"config"`
		Notes  string          `json:"notes"`
	}
	if err := bind(c, &req); err != nil {
		writeErr(c, err)
		return
	}
	actor := actorOf(c)
	ver, warnings, err := s.agents.Publish(c.Request.Context(), actor.TenantID, id, actor.UserID, req.Config, req.Notes, c.ClientIP())
	if err != nil {
		writeErr(c, err)
		return
	}
	if warnings == nil {
		warnings = []agentsvc.Warning{}
	}
	c.JSON(http.StatusCreated, gin.H{"version": versionJSON(ver, warnings), "warnings": warnings})
}

func (s *Server) getVersion(c *gin.Context) {
	id, err := ids.Parse(ids.Agent, c.Param("id"))
	if err != nil {
		writeErr(c, apperr.NotFound())
		return
	}
	n, err := strconv.Atoi(c.Param("v"))
	if err != nil {
		writeErr(c, apperr.NotFound())
		return
	}
	ver, err := s.agents.Version(c.Request.Context(), actorOf(c).TenantID, id, n)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, versionJSON(ver, nil))
}

func (s *Server) diffVersions(c *gin.Context) {
	id, err := ids.Parse(ids.Agent, c.Param("id"))
	if err != nil {
		writeErr(c, apperr.NotFound())
		return
	}
	fromN, err1 := strconv.Atoi(c.Query("from"))
	toN, err2 := strconv.Atoi(c.Query("to"))
	if err1 != nil || err2 != nil {
		writeErr(c, apperr.Invalid("from 和 to 必须是版本号"))
		return
	}
	actor := actorOf(c)
	from, err := s.agents.Version(c.Request.Context(), actor.TenantID, id, fromN)
	if err != nil {
		writeErr(c, err)
		return
	}
	to, err := s.agents.Version(c.Request.Context(), actor.TenantID, id, toN)
	if err != nil {
		writeErr(c, err)
		return
	}
	var a, b any
	if err := json.Unmarshal(from.Config, &a); err != nil {
		writeErr(c, err)
		return
	}
	if err := json.Unmarshal(to.Config, &b); err != nil {
		writeErr(c, err)
		return
	}
	var changes []gin.H
	diffValue("", a, b, &changes)
	if changes == nil {
		changes = []gin.H{}
	}
	c.JSON(http.StatusOK, gin.H{"from": fromN, "to": toN, "changes": changes})
}

func (s *Server) setCurrent(c *gin.Context) {
	id, err := ids.Parse(ids.Agent, c.Param("id"))
	if err != nil {
		writeErr(c, apperr.NotFound())
		return
	}
	var req struct {
		VersionID string `json:"version_id"`
	}
	if err := bind(c, &req); err != nil {
		writeErr(c, err)
		return
	}
	vid, err := ids.Parse(ids.Version, req.VersionID)
	if err != nil {
		writeErr(c, apperr.NotFound())
		return
	}
	actor := actorOf(c)
	agent, err := s.agents.SetCurrent(c.Request.Context(), actor.TenantID, id, vid, actor.UserID, c.ClientIP())
	if err != nil {
		writeErr(c, err)
		return
	}
	writeAgent(c, http.StatusOK, agent.Version, agentJSON(agent))
}

func (s *Server) agentMatch(c *gin.Context) (uuid.UUID, int, error) {
	id, err := ids.Parse(ids.Agent, c.Param("id"))
	if err != nil {
		return uuid.Nil, 0, apperr.NotFound()
	}
	raw := c.GetHeader("If-Match")
	if raw == "" {
		return uuid.Nil, 0, apperr.VersionMismatch()
	}
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "W/")
	raw = strings.Trim(raw, `"`)
	raw = strings.TrimPrefix(raw, "v")
	n, err := strconv.Atoi(raw)
	if err != nil {
		return uuid.Nil, 0, apperr.VersionMismatch()
	}
	return id, n, nil
}

func bind(c *gin.Context, dst any) error {
	body, err := readBody(c)
	if err != nil {
		return apperr.JSON()
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return apperr.JSON()
	}
	return nil
}

func readBody(c *gin.Context) ([]byte, error) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, err
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func writeAgent(c *gin.Context, status, version int, body any) {
	c.Header("ETag", "\"v"+strconv.Itoa(version)+"\"")
	c.JSON(status, body)
}

func agentJSON(a agentsvc.Agent) gin.H {
	var current any
	if a.Current != nil {
		current = ids.Format(ids.Version, *a.Current)
	}
	return gin.H{
		"id": ids.Format(ids.Agent, a.ID), "name": a.Name, "description": a.Description,
		"kind": a.Kind, "status": a.Status, "version": a.Version, "current_version_id": current,
		"created_at": a.CreatedAt.UTC().Format(time.RFC3339), "updated_at": a.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func versionJSON(v agentsvc.Version, warnings []agentsvc.Warning) gin.H {
	body := gin.H{
		"id": ids.Format(ids.Version, v.ID), "agent_id": ids.Format(ids.Agent, v.AgentID),
		"version": v.Version, "config": json.RawMessage(v.Config), "notes": v.Notes,
		"created_at": v.CreatedAt.UTC().Format(time.RFC3339),
	}
	if warnings != nil {
		body["warnings"] = warnings
	}
	return body
}

func keyJSON(k authsvc.APIKey, secret string) gin.H {
	body := gin.H{
		"id": ids.Format(ids.APIKey, k.ID), "name": k.Name, "prefix": k.Prefix, "role": k.Role,
		"created_at": k.CreatedAt.UTC().Format(time.RFC3339),
	}
	if secret != "" {
		body["key"] = secret
	}
	if k.ExpiresAt != nil {
		body["expires_at"] = k.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if k.RevokedAt != nil {
		body["revoked_at"] = k.RevokedAt.UTC().Format(time.RFC3339)
	}
	if k.LastUsedAt != nil {
		body["last_used_at"] = k.LastUsedAt.UTC().Format(time.RFC3339)
	}
	return body
}

func encodeCursor(t time.Time, id uuid.UUID) string {
	raw := t.UTC().Format(time.RFC3339Nano) + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(s string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	left, right, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, uuid.Nil, apperr.Invalid("cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, left)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	id, err := uuid.Parse(right)
	return t, id, err
}

func diffValue(path string, a, b any, out *[]gin.H) {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			*out = append(*out, gin.H{"path": path, "from": a, "to": b})
			return
		}
		seen := map[string]struct{}{}
		for k, v := range av {
			seen[k] = struct{}{}
			next := path + "/" + k
			if bv == nil {
				*out = append(*out, gin.H{"path": next, "from": v, "to": nil})
				continue
			}
			if ov, ok := bv[k]; ok {
				diffValue(next, v, ov, out)
			} else {
				*out = append(*out, gin.H{"path": next, "from": v, "to": nil})
			}
		}
		for k, v := range bv {
			if _, ok := seen[k]; !ok {
				*out = append(*out, gin.H{"path": path + "/" + k, "from": nil, "to": v})
			}
		}
	default:
		ab, _ := json.Marshal(a)
		bb, _ := json.Marshal(b)
		if string(ab) != string(bb) {
			*out = append(*out, gin.H{"path": path, "from": a, "to": b})
		}
	}
}

type capture struct {
	gin.ResponseWriter
	buf   bytes.Buffer
	code  int
	wrote bool
}

func (w *capture) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *capture) Write(b []byte) (int, error) {
	w.wrote = true
	w.buf.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *capture) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}
