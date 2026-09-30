package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/ecakeman/cakerdesk/internal/apperr"
	"github.com/gin-gonic/gin"
)

const maxBody = 1 << 20 // 1MiB

func WriteError(c *gin.Context, err error) {
	var ae *apperr.Error
	if errors.As(err, &ae) && ae != nil {
		c.AbortWithStatusJSON(ae.Status, gin.H{
			"error": gin.H{"code": ae.Code, "message": ae.Msg},
		})
		return
	}
	slog.Error("internal", "err", err.Error())
	c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
		"error": gin.H{"code": "internal", "message": "内部错误"},
	})
}

// BindJSON 把请求体解进 dst。
// 超过 1MiB、不是 JSON、有未知字段、一份 body 里塞了两段 JSON，都返回 400。
func BindJSON(c *gin.Context, dst any) error {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBody+1))
	if err != nil {
		return apperr.New(http.StatusBadRequest, "invalid_json", "读取请求体失败")
	}
	if len(body) > maxBody {
		return apperr.New(http.StatusBadRequest, "invalid_json", "请求体超过 1MiB")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return apperr.New(http.StatusBadRequest, "invalid_json", "请求体不是合法 JSON")
	}
	if dec.More() {
		return apperr.New(http.StatusBadRequest, "invalid_json", "请求体包含多余内容")
	}
	return nil
}

func RequestLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		slog.Info("http",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"dur_ms", time.Since(start).Milliseconds(),
		)
	}
}
